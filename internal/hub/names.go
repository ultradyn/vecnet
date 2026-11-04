package hub

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// agentNameRE is the shape of a display name an agent picks at enrol (and of
// an operator's suggestion for one): 2–32 ASCII letters, digits, spaces,
// dots, underscores and hyphens, starting and ending with a letter or digit
// (PROTOCOL §2.12). Operator-set names keep their free form.
var agentNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,30}[A-Za-z0-9]$`)

// reservedNames are skeletons no agent may take: they read as the hub, its
// operator or its software. Every operator-kind peer's name is reserved too.
var reservedNames = []string{"operator", "admin", "administrator", "max", "hub", "vecnet", "system", "root"}

// confusables folds letters that render like Latin ones (Cyrillic, Greek and a
// few symbols) and digits that read as letters onto one form, so a lookalike
// collides with the name it imitates.
var confusables = map[rune]rune{
	'а': 'a', 'в': 'b', 'е': 'e', 'ё': 'e', 'к': 'k', 'м': 'm', 'н': 'h', 'о': 'o', 'р': 'p', 'с': 'c', 'т': 't',
	'у': 'y', 'х': 'x', 'і': 'l', 'ї': 'l', 'ј': 'j', 'ѕ': 's', 'ԁ': 'd', 'ɡ': 'g', 'ӏ': 'l', 'ո': 'n', 'ս': 'u',
	'α': 'a', 'β': 'b', 'ε': 'e', 'η': 'n', 'ι': 'l', 'κ': 'k', 'ν': 'v', 'ο': 'o', 'ρ': 'p', 'τ': 't', 'υ': 'u',
	'χ': 'x', 'ω': 'w', 'ı': 'l', 'ℓ': 'l', '|': 'l',
	'0': 'o', '1': 'l', 'i': 'l', '3': 'e', '5': 's', '8': 'b',
}

// nameSkeleton is what two names are compared by: NFKC, lower case,
// confusables folded, everything but letters and digits dropped (spaces,
// separators, zero-width and format characters), then "rn" read as "m" and
// "vv" as "w". Names with equal skeletons are the same name to a reader.
func nameSkeleton(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	var b strings.Builder
	for _, r := range s {
		if c, ok := confusables[r]; ok {
			r = c
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return strings.NewReplacer("rn", "m", "vv", "w").Replace(b.String())
}

// checkAgentName refuses a name an agent may not take for peerID: a bad
// shape (422 invalid_value), a reserved name or an operator's (422
// name_reserved), or one that reads as another peer's (409 name_taken).
func checkAgentName(ctx context.Context, tx *sql.Tx, peerID, name string) error {
	if !agentNameRE.MatchString(name) {
		return invalidValue("name", "a name is 2 to 32 characters: letters, digits, space, dot, underscore and hyphen, starting and ending with a letter or digit")
	}
	sk := nameSkeleton(name)
	for _, r := range reservedNames {
		if sk == nameSkeleton(r) {
			return E(http.StatusUnprocessableEntity, "name_reserved", fmt.Sprintf("%q is reserved; choose another name", name))
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name, kind FROM peers WHERE peer_id <> ?`, peerID)
	if err != nil {
		return fmt.Errorf("hub: check name: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var other, kind string
		if err := rows.Scan(&other, &kind); err != nil {
			return fmt.Errorf("hub: check name: %w", err)
		}
		if nameSkeleton(other) != sk {
			continue
		}
		if kind == "operator" {
			return E(http.StatusUnprocessableEntity, "name_reserved", fmt.Sprintf("%q is reserved; choose another name", name))
		}
		return E(http.StatusConflict, "name_taken", fmt.Sprintf("%q is taken or reads as another peer's name; choose another", name))
	}
	return rows.Err()
}

// Enrol exchanges a one-time token for the peer's secret (auth.Enrol) and,
// when the invite left the display name open, sets name, once: it is
// required then (422 name_required), checked by checkAgentName, and recorded
// on the enrol audit row, and the directory hears of it. A name on an invite
// that set one is 422 name_not_open unless it equals that name. A refused
// name spends nothing: the token stays good for another try.
func (h *Hub) Enrol(ctx context.Context, token, name string) (peerID, secret, finalName string, err error) {
	renamed := false
	if err := h.Auth.Spendable(ctx, token); err != nil {
		return "", "", "", err
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		peerID, secret, err = h.Auth.EnrolTx(ctx, tx, token, func(peer string) (map[string]any, error) {
			var open int
			var cur, prn string
			if err := tx.QueryRowContext(ctx, `SELECT name_open, name, prn_id FROM peers WHERE peer_id = ?`, peer).Scan(&open, &cur, &prn); err != nil {
				return nil, fmt.Errorf("hub: enrol name: %w", err)
			}
			if open == 0 {
				if name != "" && name != cur {
					return nil, E(http.StatusUnprocessableEntity, "name_not_open", fmt.Sprintf("this invite set your name (%s); only the operator can rename a peer", cur))
				}
				finalName = cur
				return nil, nil
			}
			if name == "" {
				return nil, E(http.StatusUnprocessableEntity, "name_required", "this invite lets you choose your display name: enrol again with --name NAME")
			}
			if err := checkAgentName(ctx, tx, peer, name); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE peers SET name = ?, name_open = 0 WHERE peer_id = ?`, name, peer); err != nil {
				return nil, fmt.Errorf("hub: enrol name: %w", err)
			}
			// An implicit principal is named like its peer (§2.12).
			if _, err := tx.ExecContext(ctx, `UPDATE principals SET name = ? WHERE prn_id = ? AND name = ?`, name, prn, cur); err != nil {
				return nil, fmt.Errorf("hub: enrol principal name: %w", err)
			}
			if err := h.DirectoryUpdated(ctx, tx, peer, "updated", []string{"name"}); err != nil {
				return nil, err
			}
			finalName, renamed = name, true
			return map[string]any{"name": name}, nil
		})
		return err
	})
	if err != nil {
		return "", "", "", err
	}
	if renamed {
		h.notifyEnabled(ctx)
	}
	return peerID, secret, finalName, nil
}
