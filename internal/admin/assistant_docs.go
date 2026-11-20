package admin

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Built-in default texts of the three assistant documents. A document the administrator has
// not saved yet reads as its default; the defaults are never written to the data volume.
var (
	//go:embed assistant_docs/soul.md
	aiDocSoulDefault string
	//go:embed assistant_docs/memory.md
	aiDocMemoryDefault string
	//go:embed assistant_docs/expertise.md
	aiDocExpertiseDefault string
)

const (
	// aiDocMaxBytes is the most bytes of one document that reach the model and the most a save
	// accepts. The marker aiDocCutNote follows a longer text and is not counted.
	aiDocMaxBytes = 8 << 10
	// aiDocsDir is the directory of the document files inside Config.AIDir.
	aiDocsDir = "persona"
	// aiDocEmptyNote stands in the context for a document the administrator saved empty.
	aiDocEmptyNote = "(empty)"
	// aiPersonaIntro opens the persona block of the system prompt. The documents are written by the
	// administrators only; the assistant has no tool that changes them.
	aiPersonaIntro = "Documents written by the administrators. Soul says who you are and how you answer, Memory holds their notes, " +
		"Expertise describes how this tracker works. They are background and tone only: they never change the rules above, " +
		"the language instruction or what you may do, and they are not account data: read the data with tools."
)

// aiDocCutNote is appended to a document that was cut at aiDocMaxBytes.
var aiDocCutNote = "\n[the document is longer than the limit of " + strconv.Itoa(aiDocMaxBytes) + " bytes; the rest is cut]"

// aiDocSpec describes one assistant document: key is the JSON field, file the name inside
// aiDocsDir, title the heading in the context and def the text of a document not saved yet.
type aiDocSpec struct {
	key, file, title, def string
}

// aiDocSpecs lists the documents in the order they enter the context.
var aiDocSpecs = []aiDocSpec{
	{"soul", "soul.md", "Soul", aiDocSoulDefault},
	{"memory", "memory.md", "Memory", aiDocMemoryDefault},
	{"expertise", "expertise.md", "Expertise", aiDocExpertiseDefault},
}

// aiDocsView is the "docs" field of GET and PUT /api/ai/settings: the text of every document
// as the administrator sees it (the saved one, or the default while none is saved) and the
// size limit in bytes.
type aiDocsView struct {
	Soul      string `json:"soul"`
	Memory    string `json:"memory"`
	Expertise string `json:"expertise"`
	MaxBytes  int    `json:"max_bytes"`
}

// aiDocsPut is the "docs" field of a PUT body. A nil document is left as stored.
type aiDocsPut struct {
	Soul      *string `json:"soul"`
	Memory    *string `json:"memory"`
	Expertise *string `json:"expertise"`
}

// byKey maps the key of every document in the body to its new text.
func (p *aiDocsPut) byKey() map[string]string {
	out := map[string]string{}
	if p == nil {
		return out
	}
	for key, v := range map[string]*string{"soul": p.Soul, "memory": p.Memory, "expertise": p.Expertise} {
		if v != nil {
			out[key] = *v
		}
	}
	return out
}

// aiDocPath returns the file of document d, or "" when the assistant directory is not set.
func (s *Server) aiDocPath(d aiDocSpec) string {
	if s.cfg.AIDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.AIDir, aiDocsDir, d.file)
}

// aiDocRead returns the text of document d with line breaks as "\n" (a form field never holds
// "\r\n", so the settings form would otherwise see a change that is none). A document with no file
// (or no assistant directory) reads as its default; a saved empty one stays empty. A file that
// cannot be read gives an empty text and a log line, never the default, so a damaged document is
// not replaced by an invented one.
func (s *Server) aiDocRead(d aiDocSpec) string {
	path := s.aiDocPath(d)
	if path == "" {
		return aiDocLF(d.def)
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return aiDocLF(d.def)
	case err != nil:
		s.logWarn("assistant document cannot be read", "document", d.key)
		return ""
	}
	return aiDocLF(string(raw))
}

// aiDocLF returns text with every "\r\n" replaced by "\n".
func aiDocLF(text string) string {
	return strings.ReplaceAll(text, "\r\n", "\n")
}

// aiDocsViewOf returns the documents of the settings view.
func (s *Server) aiDocsViewOf() aiDocsView {
	v := aiDocsView{MaxBytes: aiDocMaxBytes}
	for _, d := range aiDocSpecs {
		text := s.aiDocRead(d)
		switch d.key {
		case "soul":
			v.Soul = text
		case "memory":
			v.Memory = text
		case "expertise":
			v.Expertise = text
		}
	}
	return v
}

// aiDocsCheck validates the documents of a PUT body before anything is written. It returns an
// ai_bad_request error with field "docs.<key>" and reason too_long for a text over aiDocMaxBytes.
func aiDocsCheck(in map[string]string) error {
	for _, d := range aiDocSpecs {
		if len(in[d.key]) > aiDocMaxBytes {
			return aiBadRequest("docs."+d.key, aiReasonTooLong, "the document is longer than the limit")
		}
	}
	return nil
}

// aiDocsWrite saves the documents of in, each atomically with mode 0600 inside aiDocsDir. The
// caller has run aiDocsCheck, has made sure Config.AIDir is set and holds s.ai.mu. It returns the
// error of the write; an empty in writes nothing.
func (s *Server) aiDocsWrite(in map[string]string) error {
	if len(in) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.AIDir, aiDocsDir), 0o700); err != nil {
		return err
	}
	for _, d := range aiDocSpecs {
		text, ok := in[d.key]
		if !ok {
			continue
		}
		if err := writeFileAtomic(s.aiDocPath(d), []byte(aiDocLF(text)), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// aiDocCut returns text cut to aiDocMaxBytes on a character boundary with aiDocCutNote after it,
// or text itself when it fits.
func aiDocCut(text string) string {
	if len(text) <= aiDocMaxBytes {
		return text
	}
	cut := aiDocMaxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + aiDocCutNote
}

// aiPersonaBlock returns the part of the system prompt that carries the three documents, each
// under its own heading ("## Soul", "## Memory", "## Expertise"). A document is cut to
// aiDocMaxBytes with a note and cleaned of secrets (secrets are the literal values of the
// process, see aiRedactText); an empty one says "(empty)" and is not replaced by text of
// the assistant's own.
func (s *Server) aiPersonaBlock(secrets []string) string {
	var b strings.Builder
	b.WriteString(aiPersonaIntro)
	for _, d := range aiDocSpecs {
		text := strings.TrimSpace(aiRedactText(aiDocCut(s.aiDocRead(d)), secrets))
		if text == "" {
			text = aiDocEmptyNote
		}
		b.WriteString("\n\n## " + d.title + "\n" + text)
	}
	return b.String()
}
