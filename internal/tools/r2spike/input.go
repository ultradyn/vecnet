//go:build spike

package main

import (
	"bufio"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"syscall"
)

func loadCredential(path string) (credential, error) {
	bad := errors.New("credential file rejected (requires user-owned regular 0600 env file and test bucket)")
	var c credential
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return c, bad
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return c, bad
	}
	f := os.NewFile(uintptr(fd), "protected-input")
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !os.SameFile(before, st) || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return c, bad
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || st.Size() > 16384 {
		return c, bad
	}
	fields := map[string]string{}
	scan := bufio.NewScanner(io.LimitReader(f, 16385))
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return c, bad
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "R2_ACCOUNT_ID", "R2_BUCKET", "R2_ENDPOINT", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY":
		default:
			return c, bad
		}
		if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
			v = v[1 : len(v)-1]
		}
		if v == "" || strings.ContainsAny(v, "\r\n\x00") || fields[k] != "" {
			return c, bad
		}
		fields[k] = v
	}
	if scan.Err() != nil || len(fields) != 5 {
		return c, bad
	}
	c = credential{fields["R2_ACCOUNT_ID"], fields["R2_BUCKET"], fields["R2_ENDPOINT"], fields["R2_ACCESS_KEY_ID"], fields["R2_SECRET_ACCESS_KEY"]}
	for _, r := range c.account {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return credential{}, bad
		}
	}
	if len(c.account) != 32 || c.bucket != "vecnet-blobs-test" {
		return credential{}, bad
	}
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Scheme != "https" || u.Host != c.account+".r2.cloudflarestorage.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return credential{}, bad
	}
	c.endpoint = strings.TrimSuffix(c.endpoint, "/")
	return c, nil
}
