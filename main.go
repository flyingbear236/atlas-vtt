package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed web/*
var web embed.FS

const (
	assetKindScene     = "scene"
	assetKindLegacyMap = "map"
	assetKindToken     = "token"
	renderModeBitmap   = "bitmap"
	renderModeTiled    = "tiled"
)

func canonicalAssetKind(kind string) (string, bool) {
	switch kind {
	case assetKindScene, assetKindLegacyMap:
		return assetKindScene, true
	case assetKindToken:
		return assetKindToken, true
	default:
		return "", false
	}
}

func isSceneRasterKind(kind string) bool {
	return kind == assetKindScene || kind == assetKindLegacyMap
}

type Asset struct {
	ID                    string           `json:"id"`
	SourceID              string           `json:"sourceId"`
	RepresentationVersion string           `json:"representationVersion"`
	Filename              string           `json:"filename,omitempty"`
	MimeType              string           `json:"mimeType,omitempty"`
	Width                 int              `json:"width"`
	Height                int              `json:"height"`
	Size                  int64            `json:"size,omitempty"`
	Levels                int              `json:"levels"`
	Kind                  string           `json:"kind"`
	RenderMode            string           `json:"renderMode"`
	TilePresence          string           `json:"tilePresence,omitempty"`
	Provenance            *AssetProvenance `json:"provenance,omitempty"`
	RetentionPolicy       string           `json:"retentionPolicy"`
	CreatedAt             int64            `json:"createdAt,omitempty"`
	OrphanSince           *int64           `json:"orphanSince,omitempty"`
}
type Token struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	FloorID  string  `json:"floorId"`
	LayerID  string  `json:"layerId"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Size     float64 `json:"size"`
	Rotation float64 `json:"rotation"`
	Color    string  `json:"color"`
	Owner    string  `json:"owner"`
	Hidden   bool    `json:"hidden"`
	Asset    string  `json:"asset"`
}
type Member struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Secret string `json:"-"`
}
type Session struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Invite           string             `json:"invite"`
	Members          map[string]*Member `json:"members"`
	Assets           map[string]Asset   `json:"assets"`
	Keys             map[string]string  `json:"keys"`
	Receipts         map[string]Receipt `json:"receipts,omitempty"`
	CampaignRevision uint64             `json:"campaignRevision"`
	Scenes           map[string]*Scene  `json:"scenes"`
}
type peer struct {
	conn          *websocket.Conn
	member        *Member
	session       string
	out           chan any
	sceneID       string
	floorID       string
	activeTokenID string
	region        *SceneRegion
	delivery      uint64
}
type Server struct {
	mu              sync.Mutex
	sessions        map[string]*Session
	peers           map[*peer]bool
	root            string
	dirty           bool
	writes          uint64
	flushInterval   time.Duration
	imageJobs       chan struct{}
	jobContext      context.Context
	jobCancel       context.CancelFunc
	jobWG           sync.WaitGroup
	rotationJobs    map[string]*rotationJob
	stopping        bool
	storageDegraded bool
}

func id() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func newServer(root string) (*Server, error) {
	jobContext, jobCancel := context.WithCancel(context.Background())
	s := &Server{sessions: map[string]*Session{}, peers: map[*peer]bool{}, root: root, flushInterval: persistenceFlushInterval, imageJobs: make(chan struct{}, 1), jobContext: jobContext, jobCancel: jobCancel, rotationJobs: map[string]*rotationJob{}}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0755); err != nil {
		return nil, fmt.Errorf("storage directory: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "sessions.json"))
	if err == nil {
		if err = json.Unmarshal(b, &s.sessions); err != nil {
			return nil, fmt.Errorf("invalid saved sessions: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read sessions: %w", err)
	} else if _, tmpErr := os.Stat(filepath.Join(root, "sessions.json.tmp")); tmpErr == nil {
		return nil, fmt.Errorf("sessions.json is missing but a temporary save exists; restore it explicitly before starting")
	}
	if s.sessions == nil {
		return nil, fmt.Errorf("invalid saved sessions: null root")
	}
	migrated := false
	for sessionID, ss := range s.sessions {
		if ss == nil || ss.ID != sessionID || ss.Invite == "" || len(ss.Members) == 0 || len(ss.Keys) == 0 || ss.Assets == nil || ss.Scenes == nil || ss.CampaignRevision == 0 {
			return nil, fmt.Errorf("invalid session %s", sessionID)
		}
		if ss.Receipts == nil {
			ss.Receipts = map[string]Receipt{}
		}
		for assetID, asset := range ss.Assets {
			if asset.RenderMode == "" {
				if isSceneRasterKind(asset.Kind) && asset.Levels > 0 {
					asset.RenderMode = renderModeTiled
				} else {
					asset.RenderMode = renderModeBitmap
				}
				migrated = true
			}
			if asset.SourceID == "" {
				asset.SourceID = asset.ID
				migrated = true
			}
			if asset.RepresentationVersion == "" {
				asset.RepresentationVersion = "legacy"
				migrated = true
			}
			if asset.ID != assetID || asset.Width <= 0 || asset.Height <= 0 || (!isSceneRasterKind(asset.Kind) && asset.Kind != assetKindToken) || (asset.RenderMode != renderModeBitmap && asset.RenderMode != renderModeTiled) || (asset.RenderMode == renderModeTiled && (!isSceneRasterKind(asset.Kind) || asset.Levels < 1)) || !validTilePresence(asset) {
				return nil, fmt.Errorf("invalid asset in session %s", sessionID)
			}
			if asset.RetentionPolicy == "" {
				asset.RetentionPolicy = assetReclaimable
				migrated = true
			}
			if asset.RetentionPolicy != assetKeep && asset.RetentionPolicy != assetReclaimable {
				return nil, fmt.Errorf("invalid asset retention in session %s", sessionID)
			}
			if asset.MimeType == "" {
				asset.MimeType = "image/png"
				migrated = true
			}
			if asset.Filename == "" {
				asset.Filename = "image.png"
				migrated = true
			}
			ss.Assets[assetID] = asset
		}
		for assetID, asset := range ss.Assets {
			if provenance := asset.Provenance; provenance != nil && (provenance.Operation != "fixRotation" || provenance.SourceAssetID == "" || provenance.SourceAssetID == assetID || ss.Assets[provenance.SourceAssetID].ID == "" || provenance.RecipeHash == "" || provenance.RecipeVersion == "") {
				return nil, fmt.Errorf("invalid derived asset in session %s", sessionID)
			}
		}
		for sceneID, scene := range ss.Scenes {
			if scene == nil || scene.ID != sceneID || scene.Name == "" {
				return nil, fmt.Errorf("invalid scene in session %s", sessionID)
			}
			if ensureSceneStructure(scene) {
				migrated = true
			}
			if !validateSceneStructure(scene, ss.Assets, ss.Members) {
				return nil, fmt.Errorf("invalid scene %s in session %s", sceneID, sessionID)
			}
			scene.rebuildRuntime()
		}
		for memberID, m := range ss.Members {
			if m == nil || m.ID != memberID || (m.Role != "gm" && m.Role != "player") {
				return nil, fmt.Errorf("invalid member in session %s", sessionID)
			}
		}
		for k, v := range ss.Keys {
			if m := ss.Members[v]; m != nil {
				m.Secret = k
			} else {
				return nil, fmt.Errorf("invalid member key in session %s", sessionID)
			}
		}
		if refreshAssetOrphans(ss, time.Now()) {
			migrated = true
		}
	}
	if migrated {
		s.dirty = true
		if err := s.saveLocked(); err != nil {
			return nil, fmt.Errorf("save migrated sessions: %w", err)
		}
	}
	return s, nil
}
func (s *Server) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}
func (s *Server) saveLocked() error {
	if !s.dirty {
		return nil
	}
	b, e := json.Marshal(s.sessions)
	if e != nil {
		return e
	}
	path := filepath.Join(s.root, "sessions.json")
	f, e := os.OpenFile(path+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(path+".tmp", path); e != nil {
		return e
	}
	s.dirty = false
	s.writes++
	return nil
}
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, msg string) { http.Error(w, msg, code) }
func (s *Server) auth(r *http.Request) (*Session, *Member) {
	ss := s.sessions[r.URL.Query().Get("session")]
	if ss == nil {
		return nil, nil
	}
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if m := ss.Members[ss.Keys[key]]; m != nil {
		return ss, m
	}
	return nil, nil
}
func (s *Server) send(p *peer, v any) {
	select {
	case p.out <- v:
	default:
		p.conn.Close()
	}
}

func (s *Server) publishPresence(ss *Session, changed *Member, online bool, except *peer) {
	for p := range s.peers {
		if p == except || p.session != ss.ID {
			continue
		}
		s.send(p, map[string]any{"type": "presence", "member": changed, "online": online})
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", s.create)
	mux.HandleFunc("POST /api/join", s.join)
	mux.HandleFunc("POST /api/upload", s.uploadAsset)
	mux.HandleFunc("GET /api/asset/", s.asset)
	mux.HandleFunc("GET /ws", s.ws)
	sub, _ := fs.Sub(web, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(v) != nil {
		fail(w, 400, "Некорректный запрос")
		return false
	}
	return true
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	if !decode(w, r, &req) {
		return
	}
	if len(req.Name) > 120 {
		fail(w, 400, "Слишком длинное название")
		return
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		fail(w, 503, "Сервер останавливается")
		return
	}
	m := &Member{ID: id(), Name: "Ведущий", Role: "gm", Secret: id()}
	sceneID := id()
	scene := newScene(sceneID, "Сцена 1")
	scene.Published = true
	ss := &Session{ID: id(), Name: req.Name, Invite: id(), Members: map[string]*Member{m.ID: m}, Keys: map[string]string{m.Secret: m.ID}, Assets: map[string]Asset{}, CampaignRevision: 1, Scenes: map[string]*Scene{sceneID: scene}}
	if ss.Name == "" {
		ss.Name = "Новая история"
	}
	oldDirty := s.dirty
	s.sessions[ss.ID] = ss
	s.dirty = true
	if s.saveLocked() != nil {
		delete(s.sessions, ss.ID)
		s.dirty = oldDirty
		s.mu.Unlock()
		fail(w, 500, "Ошибка сохранения")
		return
	}
	s.mu.Unlock()
	reply(w, map[string]any{"session": ss.ID, "key": m.Secret, "invite": ss.Invite})
}
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var req struct{ Session, Invite, Name string }
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		fail(w, 503, "Сервер останавливается")
		return
	}
	ss := s.sessions[req.Session]
	if ss == nil || ss.Invite != req.Invite {
		s.mu.Unlock()
		fail(w, 403, "Приглашение недействительно")
		return
	}
	if len(req.Name) > 80 || strings.TrimSpace(req.Name) == "" {
		s.mu.Unlock()
		fail(w, 400, "Укажите имя до 80 символов")
		return
	}
	m := &Member{ID: id(), Name: req.Name, Role: "player", Secret: id()}
	oldRevision, oldDirty := ss.CampaignRevision, s.dirty
	ss.Members[m.ID] = m
	ss.Keys[m.Secret] = m.ID
	ss.CampaignRevision++
	s.dirty = true
	if s.saveLocked() != nil {
		delete(ss.Members, m.ID)
		delete(ss.Keys, m.Secret)
		ss.CampaignRevision, s.dirty = oldRevision, oldDirty
		s.mu.Unlock()
		fail(w, 500, "Ошибка сохранения")
		return
	}
	s.mu.Unlock()
	reply(w, map[string]string{"session": ss.ID, "key": m.Secret})
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	c, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer c.Close()
	c.SetReadLimit(16384)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var hello struct {
		Session string
		Key     string
	}
	if c.ReadJSON(&hello) != nil {
		return
	}
	s.mu.Lock()
	ss := s.sessions[hello.Session]
	if ss == nil {
		s.mu.Unlock()
		c.WriteJSON(map[string]string{"type": "fatal", "message": "Сессия удалена или не существует"})
		return
	}
	m := ss.Members[ss.Keys[hello.Key]]
	if m == nil {
		s.mu.Unlock()
		c.WriteJSON(map[string]string{"type": "fatal", "message": "Неверный ключ доступа. Войдите по действующему приглашению"})
		return
	}
	if s.stopping {
		s.mu.Unlock()
		return
	}
	p := &peer{
		conn:    c,
		member:  m,
		session: ss.ID,
		out:     make(chan any, 128),
	}
	s.peers[p] = true
	s.send(p, s.snapshotCampaign(ss, p.member))
	if s.storageDegraded {
		s.send(p, map[string]string{"type": "storageError", "message": storageDegradedMessage})
	}
	s.publishPresence(ss, m, true, p)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.peers, p)
		stillOnline := false
		for other := range s.peers {
			if other.session == ss.ID && other.member.ID == m.ID {
				stillOnline = true
				break
			}
		}
		s.publishPresence(ss, m, stillOnline, nil)
		s.mu.Unlock()
	}()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case v := <-p.out:
				c.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if c.WriteJSON(v) != nil {
					c.Close()
					return
				}
			case <-ticker.C:
				if c.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)) != nil {
					c.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	for {
		var msg Command
		if c.ReadJSON(&msg) != nil {
			return
		}
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return
		}
		if msg.Type == "sync" {
			s.send(p, s.snapshotSceneForPeer(ss, p))
		} else if msg.Type == "view" {
			if p.sceneID == "" || msg.SceneID != p.sceneID || msg.Region == nil || !msg.Region.valid() {
				s.send(p, map[string]string{"type": "error", "message": "Некорректная область сцены"})
			} else {
				scene := ss.Scenes[p.sceneID]
				if m.Role == "gm" && msg.ViewFloorID != "" {
					if scene.Floors[msg.ViewFloorID].ID == "" {
						s.send(p, map[string]string{"type": "error", "message": "Этаж не найден"})
						s.mu.Unlock()
						continue
					}
					p.floorID = msg.ViewFloorID
				}
				p.floorID = currentFloorForPeer(p, scene)
				region := *msg.Region
				p.region = &region
				s.send(p, s.snapshotSceneForPeer(ss, p))
			}
		} else if msg.Type == "activeToken" {
			if p.sceneID == "" || msg.SceneID != p.sceneID || m.Role != "player" {
				s.send(p, map[string]string{"type": "error", "operation": "activeToken", "message": "Токен нельзя сделать активным"})
			} else if token, ok := activeTokenForMember(ss.Scenes[p.sceneID], m, msg.ActiveTokenID); !ok {
				s.send(p, map[string]string{"type": "error", "operation": "activeToken", "message": "Токен не принадлежит игроку"})
			} else if msg.Region != nil && !msg.Region.valid() {
				s.send(p, map[string]string{"type": "error", "operation": "activeToken", "message": "Некорректная область сцены"})
			} else {
				p.activeTokenID = token.ID
				p.floorID = tokenFloorID(ss.Scenes[p.sceneID], token)
				if msg.Region != nil {
					region := *msg.Region
					if msg.Focus {
						width, height := region.Right-region.Left, region.Bottom-region.Top
						region = SceneRegion{Left: token.X - width/2, Top: token.Y - height/2, Right: token.X + width/2, Bottom: token.Y + height/2}
					}
					p.region = &region
				}
				s.send(p, s.snapshotSceneForPeer(ss, p))
			}
		} else if msg.Type == "subscribe" {
			if msg.SceneID == "" {
				p.sceneID = ""
				p.floorID = ""
				p.activeTokenID = ""
				p.region = nil
				p.delivery = 0
				s.send(p, s.snapshotCampaign(ss, m))
			} else if scene := ss.Scenes[msg.SceneID]; scene == nil || (m.Role != "gm" && !scene.Published) {
				s.send(p, map[string]string{"type": "error", "message": "Сцена недоступна"})
			} else {
				p.sceneID = msg.SceneID
				p.activeTokenID = msg.ActiveTokenID
				p.floorID = currentFloorForPeer(p, scene)
				p.region = nil
				p.delivery = 0
				s.send(p, s.snapshotSceneForPeer(ss, p))
			}
		} else {
			s.command(ss, p, msg)
		}
		s.mu.Unlock()
	}
}

func validNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 1000000
}
func (s *Server) uploadAsset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss, m := s.auth(r)
	sceneID := r.URL.Query().Get("scene")
	scene := ssScene(ss, sceneID)
	allowed := m != nil && m.Role == "gm" && scene != nil
	s.mu.Unlock()
	if !allowed {
		fail(w, 403, "Нужны права ведущего")
		return
	}
	select {
	case s.imageJobs <- struct{}{}:
		defer func() { <-s.imageJobs }()
	default:
		fail(w, 429, "Другая карта уже обрабатывается")
		return
	}
	kind, validKind := canonicalAssetKind(r.URL.Query().Get("kind"))
	if !validKind {
		fail(w, 400, "Неизвестный тип")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit)
	a, e := prepareReader(r.Context(), s.root, r.Body, kind)
	if e != nil {
		status := http.StatusBadRequest
		var maxBytes *http.MaxBytesError
		if errors.Is(e, errUploadSize) || errors.As(e, &maxBytes) {
			status = http.StatusRequestEntityTooLarge
		}
		if errors.Is(e, errVipsMissing) {
			status = http.StatusServiceUnavailable
		}
		fail(w, status, e.Error())
		return
	}
	if r.Context().Err() != nil {
		return
	}
	if filename := strings.TrimSpace(r.URL.Query().Get("name")); filename != "" && len(filename) <= 200 && !strings.ContainsAny(filename, "/\\") {
		a.Filename = filename
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		fail(w, 503, "Сервер останавливается")
		return
	}
	if r.Context().Err() != nil {
		s.mu.Unlock()
		return
	}
	currentSession, currentMember := s.auth(r)
	if currentSession != ss || currentMember == nil || currentMember.Role != "gm" || ss.Scenes[sceneID] != scene {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "Сцена была закрыта во время загрузки")
		return
	}
	oldAsset, hadAsset := ss.Assets[a.ID]
	oldRevision, oldDirty, oldBounds := scene.Revision, s.dirty, scene.Bounds
	oldElements := make(map[string]SceneElement, len(scene.Elements))
	for elementID, element := range scene.Elements {
		oldElements[elementID] = element
	}
	oldLayers := make(map[string]Layer, len(scene.Layers))
	for layerID, layer := range scene.Layers {
		oldLayers[layerID] = layer
	}
	if hadAsset {
		a.RetentionPolicy = oldAsset.RetentionPolicy
		a.CreatedAt = oldAsset.CreatedAt
		a.OrphanSince = oldAsset.OrphanSince
	}
	ss.Assets[a.ID] = a
	if kind == assetKindScene {
		floorID := r.URL.Query().Get("floor")
		layerID := r.URL.Query().Get("layer")
		if scene.Floors[floorID].ID == "" {
			floorID = firstFloorID(scene)
		}
		if layer := scene.Layers[layerID]; layer.ID == "" || layer.Kind != layerKindVisual || layer.FloorID != floorID {
			layerID = firstLayerID(scene, floorID)
		}
		elementID := id()
		name := strings.TrimSuffix(a.Filename, filepath.Ext(a.Filename))
		if name == "" {
			name = "Изображение"
		}
		scene.Elements[elementID] = SceneElement{ID: elementID, FloorID: floorID, LayerID: layerID, AssetID: a.ID, Name: name, Transform: Transform{Width: float64(a.Width), Height: float64(a.Height)}, Visible: true, Opacity: 1}
		if len(oldElements) == 0 && len(scene.Tokens) == 0 {
			scene.Bounds = SceneBounds{Width: float64(a.Width), Height: float64(a.Height)}
			walkableID := layerIDByKind(scene, floorID, layerKindWalkable)
			walkable := scene.Layers[walkableID]
			if walkable.WalkableBounds != nil && *walkable.WalkableBounds == (WalkableBounds{Width: oldBounds.Width, Height: oldBounds.Height}) {
				walkable.WalkableBounds = defaultWalkableBounds(scene.Bounds)
				scene.Layers[walkableID] = walkable
			}
		}
		scene.Revision++
		scene.rebuildRuntime()
	}
	refreshAssetOrphans(ss, time.Now())
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		if hadAsset {
			ss.Assets[a.ID] = oldAsset
		} else {
			delete(ss.Assets, a.ID)
		}
		scene.Elements, scene.Bounds, scene.Revision, s.dirty = oldElements, oldBounds, oldRevision, oldDirty
		scene.Layers = oldLayers
		scene.rebuildRuntime()
		s.mu.Unlock()
		fail(w, 500, "Ошибка сохранения")
		return
	}
	if kind == assetKindScene {
		s.publishSceneSnapshot(ss, sceneID)
	}
	s.mu.Unlock()
	reply(w, a)
}
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/asset/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	ss, m := s.auth(r)
	allowed := ss != nil && m != nil && assetVisibleToAtToken(ss, m, r.URL.Query().Get("scene"), parts[0], r.URL.Query().Get("activeTokenId"))
	s.mu.Unlock()
	if !allowed {
		fail(w, 403, "Ассет недоступен")
		return
	}
	name := parts[1]
	if strings.ContainsAny(name, "/\\:") || (!strings.HasSuffix(name, ".png")) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(s.root, "assets", parts[0], name))
}
func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	root := flag.String("data", "data", "persistent storage directory")
	flag.Parse()
	s, err := newServer(*root)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("VTT: http://%s", *addr)
	if err = s.serve(ctx, listener); err != nil {
		log.Fatal(err)
	}
}
