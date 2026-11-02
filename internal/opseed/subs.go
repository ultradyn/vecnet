package opseed

import (
	"encoding/json"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/webhook"
)

func (b *builder) subscriptions() error {
	var prn string
	if err := b.h.DB.QueryRowContext(b.ctx, `SELECT prn_id FROM peers WHERE peer_id=?`, b.s.Peers["dot"]).Scan(&prn); err != nil {
		return err
	}
	// The demo models a linked ChatGPT account, without minting a dot bearer.
	if _, err := b.h.DB.ExecContext(b.ctx, `INSERT INTO oauth_principals(prn_id,subject,peer_id,created_at) VALUES(?,?,?,?)`, prn, "oauth:demo", b.s.Peers["dot"], envelope.FormatTime(b.h.Now())); err != nil {
		return err
	}
	var epoch int
	var seq int
	if err := b.h.DB.QueryRowContext(b.ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='epoch'`).Scan(&epoch); err != nil {
		return err
	}
	if err := b.h.DB.QueryRowContext(b.ctx, `SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&seq); err != nil {
		return err
	}
	for i, reason := range []string{"", "", "token_expired", "delivery_failed", "unauthorized", "operator"} {
		id := ids.New(ids.Prefix("sub"))
		secret := webhook.NewSecret()
		sealed, err := b.h.Seal([]byte(secret))
		if err != nil {
			return err
		}
		expiry := b.h.Now().Add(time.Hour)
		if i == 1 {
			expiry = b.h.Now().Add(2 * time.Minute)
		}
		if i == 2 {
			expiry = b.h.Now().Add(-time.Minute)
		}
		var paused any
		if reason != "" {
			paused = reason
		}
		args, _ := json.Marshal(map[string]string{"peer": b.s.Peers["dot"]})
		now := envelope.FormatTime(b.h.Now())
		// Give each subscription a real token-hash row. No token plaintext is
		// exposed: the fixture only needs revocation/expiry checks to behave.
		_, tokenHash := auth.NewSecret("oauth")
		var revoked any
		if reason == "unauthorized" {
			revoked = now
		}
		if _, err := b.h.DB.ExecContext(b.ctx, `INSERT INTO oauth_tokens(token_hash,kind,client_id,principal_id,scope,resource,expires_at,revoked_at,created_at) VALUES(?,'access','demo',?,'vecnet',?,?,?,?)`, tokenHash, prn, b.h.Cfg.PublicURL+"/mcp", envelope.FormatTime(expiry), revoked, now); err != nil {
			return err
		}
		// Match Store.Upsert: subscription expiry cannot outlive the OAuth token.
		_, err = b.h.DB.ExecContext(b.ctx, `INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,expires_at,verified_at,paused_reason,token_expires_at,last_delivery_at,last_status,created_at,updated_at,token_hash,last_refresh_at) VALUES(?,'oauth',?,?,'vecnet.message',?,?,?,?,?,'["delivery"]',?,?,?,?,?,200,?,?,?,?)`, id, prn, b.s.Peers["dot"], string(args), b.h.Cfg.PublicURL+"/__demo/sink/"+id, sealed, epoch, seq, envelope.FormatTime(expiry), now, paused, envelope.FormatTime(expiry), now, now, now, tokenHash, now)
		if err != nil {
			return err
		}
		b.s.Subscriptions = append(b.s.Subscriptions, id)
	}
	return nil
}
