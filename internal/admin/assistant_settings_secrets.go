package admin

import (
	"context"
	"os"
	"sort"
	"strings"
)

// aiSecretMinLen is the shortest literal secret that is listed and searched for in text.
// A shorter value would mask ordinary words.
const aiSecretMinLen = 8

// aiSecretsOr returns secrets when the caller hands over the list its turn already built, and
// the secrets of the process (processSecrets) when it passes nil. A list built by processSecrets
// is never nil, so an empty one still counts as handed over.
func (s *Server) aiSecretsOr(secrets []string) []string {
	if secrets != nil {
		return secrets
	}
	return s.processSecrets()
}

// aiSecretsKey is the context key of the list of secrets one request collected.
type aiSecretsKey struct{}

// aiWithSecrets returns ctx carrying the secrets of the process, collected once. A ctx that
// already carries a list is returned as it is, so a run of calls under one request shares one
// collection.
func (s *Server) aiWithSecrets(ctx context.Context) context.Context {
	if _, ok := ctx.Value(aiSecretsKey{}).([]string); ok {
		return ctx
	}
	return context.WithValue(ctx, aiSecretsKey{}, s.processSecrets())
}

// aiSecretsFrom returns the list of secrets ctx carries (see aiWithSecrets), or collects the
// secrets of the process when it carries none.
func (s *Server) aiSecretsFrom(ctx context.Context) []string {
	if secrets, ok := ctx.Value(aiSecretsKey{}).([]string); ok {
		return secrets
	}
	return s.processSecrets()
}

// processSecrets returns every literal secret of the server that must never reach a
// model or a stored chat: the provider keys of ai.json, the Cloudflare token, the checker token
// and the checker postback token, the Namecheap key, the Keitaro keys of the file and of the Config, Config.SecretValues, the content of
// the files Config.PasswordFile and Config.CIDKeyFile, the administrator password hash and
// the password hashes of users. It reads the Config and those files only, never the
// environment. Values shorter than aiSecretMinLen are left out, as are empty ones. The list has no
// duplicates and starts with the longest values, so a replacement never leaves the tail
// of a secret behind.
func (s *Server) processSecrets() []string {
	set := make(map[string]struct{})
	add := func(v string) {
		if v = strings.TrimSpace(v); len(v) >= aiSecretMinLen {
			set[v] = struct{}{}
		}
	}
	if st, err := s.aiLoad(); err == nil {
		for _, k := range st.Keys {
			add(k)
		}
	}
	add(s.cloudflareAccount().Token)
	checker := s.checkerSettings()
	add(checker.Token)
	add(checker.PostbackToken)
	add(s.namecheapAccount().APIKey)
	add(s.keitaroCreds().Key)
	for _, c := range s.listKeitaroConns() {
		add(c.Key)
	}
	add(s.cfg.KeitaroKey)
	add(s.cfg.PasswordHash)
	for _, v := range s.cfg.SecretValues {
		add(v)
	}
	add(s.current().hash)
	for _, path := range []string{s.cfg.PasswordFile, s.cfg.CIDKeyFile} {
		if path == "" {
			continue
		}
		if raw, err := os.ReadFile(path); err == nil {
			add(string(raw))
		}
	}
	if s.users != nil {
		for _, pub := range s.users.List() {
			if u, ok := s.userByID(pub.ID); ok {
				add(u.Hash)
			}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}
