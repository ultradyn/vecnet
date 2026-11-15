package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestPeerArchiveSharedRevocation(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "sweep"}[automatic], func(t *testing.T) {
			env := testhub.Start(t)
			ctx := context.Background()
			p, _ := env.NewPeer("session", "revoked")
			now := env.Now.Now()
			at := envelope.FormatTime(now)
			old := envelope.FormatTime(now.Add(-3 * 24 * time.Hour))
			archiveExec(t, env, `INSERT INTO enrol_tokens(token_hash,peer_id,expires_at) VALUES('enrol-archive',?,?)`, p, at)
			archiveExec(t, env, `INSERT INTO dot_links(code_hash,peer_id,expires_at) VALUES('link-archive',?,?)`, p, at)
			var prn string
			if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, p).Scan(&prn); err != nil {
				t.Fatal(err)
			}
			archiveExec(t, env, `INSERT INTO oauth_principals(prn_id,subject,peer_id,created_at) VALUES(?,'archive-subject',?,?)`, prn, p, at)
			archiveExec(t, env, `INSERT INTO oauth_tokens(token_hash,kind,client_id,principal_id,scope,resource,expires_at,created_at) VALUES('access-archive','access','client',?,'scope','resource',?,?),('refresh-archive','refresh','client',?,'scope','resource',?,?)`, prn, at, at, prn, at, at)
			archiveExec(t, env, `UPDATE peers SET last_seen=?,enrolled_at=? WHERE peer_id=?`, old, old, p)
			if automatic {
				if n, err := env.Hub.ArchiveIdlePeers(ctx, now); err != nil || n != 1 {
					t.Fatalf("sweep=%d %v", n, err)
				}
			} else if err := env.Hub.SetPeerArchived(ctx, p, true); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{`SELECT count(*) FROM enrol_tokens WHERE peer_id=? AND used_at IS NULL`, `SELECT count(*) FROM dot_links WHERE peer_id=? AND used_at IS NULL`, `SELECT count(*) FROM credentials WHERE subject_id=? AND revoked_at IS NULL`, `SELECT count(*) FROM oauth_tokens WHERE revoked_at IS NULL AND principal_id IN(SELECT prn_id FROM oauth_principals WHERE peer_id=?)`} {
				if n := archiveCount(t, env, q, p); n != 0 {
					t.Fatalf("unrevoked %d: %s", n, q)
				}
			}
			if n := archiveCount(t, env, `SELECT count(*) FROM events WHERE type='directory_updated' AND json_extract(data,'$.peer_id')=? AND json_extract(data,'$.change')='removed'`, p); n < 1 {
				t.Fatal("directory removal missing")
			}
		})
	}
}
