package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// exportPortablePackage captures the mechanical state briefly under the lock,
// then builds and serves the archive without blocking game commands.
func (s *Server) exportPortablePackage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	session, member := s.auth(r)
	if !memberIsGM(member) {
		s.mu.Unlock()
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	snapshot := snapshotPortableExport(session)
	s.mu.Unlock()

	includeMedia := r.URL.Query().Get("includeMedia") != "false"
	file, err := os.CreateTemp(s.root, "atlas-export-*.zip")
	if err != nil {
		fail(w, http.StatusInternalServerError, "Не удалось создать временный архив")
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := ExportPortablePackage(r.Context(), snapshot, s.root, includeMedia, file); err != nil {
		fail(w, http.StatusConflict, fmt.Sprintf("Ошибка экспорта: %v", err))
		return
	}
	if _, err := file.Seek(0, 0); err != nil {
		fail(w, http.StatusInternalServerError, "Не удалось прочитать архив")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="atlas-export.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "atlas-export.zip", time.Time{}, file)
}

// downloadCurrentDefinitionFile uses the same on-demand source-preserving
// renderer as the existing text-view API, but provides a download filename.
func (s *Server) downloadCurrentDefinitionFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	session, member := s.auth(r)
	if !memberIsGM(member) {
		s.mu.Unlock()
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	name := r.URL.Query().Get("name")
	var data string
	var err error
	data, err = RenderRulesetFileTOML(session.Ruleset, name)
	s.mu.Unlock()
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	if name == "" || strings.ContainsAny(name, "\\\r\n\"") {
		fail(w, http.StatusBadRequest, "Некорректное имя файла")
		return
	}
	filename := name
	w.Header().Set("Content-Type", "application/toml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(data))
}
