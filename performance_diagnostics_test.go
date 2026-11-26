package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

type diagnosticScale struct {
	name                                                  string
	campaigns, characters, stats, items, actions, history int
}

var diagnosticScales = []diagnosticScale{
	{"normal", 1, 5, 30, 20, 10, 30},
	{"large", 5, 20, 100, 100, 30, 200},
	{"stress", 10, 50, 200, 300, 60, 500},
}

// Synthetic v0.4 data: independent computed stats, direct actions, equipped
// item actions and effects. No production files or running server are needed.
func diagnosticFixture(t testing.TB, p diagnosticScale) (*Server, *Session, *Member) {
	t.Helper()
	var src strings.Builder
	src.WriteString("[stat.hp]\ndefault = 100\nresource = true\n[action.inspect]\ntarget = 'select(object)'\nmessage = 'Inspect {target.name}'\n[action.item_hit]\nroll = '1d20 + stat.s000'\n[effect.item_bonus]\ntarget = ['action.item_hit.roll']\nformula = 'target + 1'\nscope = 'source'\n[object.gear]\nname = 'Gear'\nactions = ['action.item_hit']\neffects = ['effect.item_bonus']\n")
	for i := 0; i < p.stats-1; i++ {
		fmt.Fprintf(&src, "[stat.s%03d]\ndefault = %d\nbonus = { formula = 'value + 1' }\n", i, i+1)
	}
	for i := 0; i < p.actions; i++ {
		fmt.Fprintf(&src, "[action.a%03d]\nroll = '1d20 + stat.s%03d.bonus'\n", i, i%(p.stats-1))
	}
	rules, err := CompileRulesetFiles(map[string]string{"rules.toml": src.String()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{root: t.TempDir(), sessions: map[string]*Session{}}
	gm := &Member{ID: "gm", Name: "GM", GM: true, Role: "gm"}
	player := &Member{ID: "player", Name: "Player", Role: "player"}
	var first *Session
	for c := 0; c < p.campaigns; c++ {
		sid := fmt.Sprintf("campaign-%d", c)
		scene := newScene("scene", "Diagnostic scene")
		scene.Published = true
		ss := &Session{ID: sid, Name: sid, Members: map[string]*Member{gm.ID: gm, player.ID: player}, Scenes: map[string]*Scene{scene.ID: scene}, Ruleset: rules, Assets: map[string]Asset{}}
		initializeSessionCharacterCollections(ss)
		for j := 0; j < p.characters; j++ {
			cid := fmt.Sprintf("character-%04d", j)
			ch := NewCharacterInstance(cid, nil)
			ch.Name = cid
			ch.Persistent = true
			ch.Materialized.ActionIDs = []string{"inspect"}
			for k := 0; k < p.actions; k++ {
				ch.Materialized.ActionIDs = append(ch.Materialized.ActionIDs, fmt.Sprintf("a%03d", k))
			}
			ch.ResourceCurrent = map[string]StatValue{"hp": IntegerStatValue(100)}
			for k := 0; k < p.items; k++ {
				ch.Inventory = append(ch.Inventory, InventoryItem{ID: fmt.Sprintf("item-%04d", k), ObjectID: "gear", Equipped: k%4 == 0})
			}
			ss.CharacterInstances[cid] = ch
			floor := firstFloorID(scene)
			token := Token{ID: cid, Name: cid, CharacterInstanceID: cid, FloorID: floor, LayerID: layerIDByKind(scene, floor, layerKindTokens), X: 100 + float64(j)*80, Y: 100, Size: 64, OwnerIDs: []string{player.ID}}
			scene.Tokens[token.ID] = token
		}
		scene.rebuildRuntime()
		rebuildCharacterReferences(ss)
		// Synthetic valid journal entries, 2 KiB each; intentionally not a replay.
		ss.ActionRevision = uint64(p.history)
		for j := 1; j <= p.history; j++ {
			ss.ActionHistory = append(ss.ActionHistory, actionJournalEntry{ActionEvent: ActionEvent{ExecutionID: fmt.Sprintf("event-%d", j), UserID: gm.ID, AuthorName: gm.Name, CharacterID: "character-0000", ActionID: "inspect", SceneID: scene.ID, Revision: uint64(j), Timestamp: time.Unix(int64(j), 0)}, Stream: "gm:diagnostic", Seq: uint64(j), Digest: strings.Repeat("a", 64), Details: json.RawMessage(`{"payload":"` + strings.Repeat("x", 2048) + `"}`)})
		}
		s.sessions[sid] = ss
		if first == nil {
			first = ss
		}
	}
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		t.Fatalf("invalid diagnostic fixture: %v", err)
	}
	return s, first, gm
}

func diagnosticJSON(t testing.TB, v any) int {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}
func diagnosticSamples(t *testing.T, name string, n int, fn func(int)) {
	t.Helper()
	fn(0) // warmup
	durations := make([]time.Duration, n)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range durations {
		start := time.Now()
		fn(i)
		durations[i] = time.Since(start)
	}
	runtime.ReadMemStats(&after)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	quantile := func(percent int) float64 { return float64(durations[(n*percent+99)/100-1]) / float64(time.Millisecond) }
	t.Logf("METRIC operation=%s samples=%d p50_ms=%.3f p95_ms=%.3f max_ms=%.3f allocated_bytes_per_op=%d mallocs_per_op=%d", name, n, quantile(50), quantile(95), quantile(100), (after.TotalAlloc-before.TotalAlloc)/uint64(n), (after.Mallocs-before.Mallocs)/uint64(n))
}

func TestAtlasPerformanceDiagnostics(t *testing.T) {
	for _, p := range diagnosticScales {
		t.Run(p.name, func(t *testing.T) {
			s, ss, gm := diagnosticFixture(t, p)
			cid := "character-0000"
			player := ss.Members["player"]
			t.Logf("PROFILE campaigns=%d characters_per_campaign=%d stats=%d inventory_per_character=%d direct_actions=%d journal_per_campaign=%d", p.campaigns, p.characters, p.stats, p.items, p.actions+1, p.history)
			const samples = 30
			diagnosticSamples(t, "registry_merge", samples, func(_ int) {
				if _, err := MergeDefinitionRegistries(sessionRegistries(ss)); err != nil {
					t.Fatal(err)
				}
			})
			for _, member := range []*Member{gm, player} {
				var snap characterSnapshot
				diagnosticSamples(t, "snapshot_"+member.ID, samples, func(_ int) {
					var err error
					snap, err = characterSnapshotForMember(ss, member, cid, 1)
					if err != nil {
						t.Fatal(err)
					}
				})
				if snap.CharacterID != cid || len(snap.Instance.Inventory) != p.items {
					t.Fatal("incomplete snapshot")
				}
				t.Logf("PAYLOAD operation=snapshot_%s json_bytes=%d traces=%d previews=%d", member.ID, diagnosticJSON(t, snap), len(snap.StatTraces), len(snap.ActionPreviews))
				diagnosticSamples(t, "snapshot_json_"+member.ID, samples, func(_ int) { diagnosticJSON(t, snap) })
			}
			for _, member := range []*Member{gm, player} {
				watcher := &peer{member: member, session: ss.ID, out: make(chan any, 1)}
				var payload any
				diagnosticSamples(t, "snapshot_initial_"+member.ID, samples, func(i int) {
					s.watchCharacter(ss, watcher, cid, uint64(i+1), true)
					payload = <-watcher.out
					diagnosticJSON(t, payload)
				})
				t.Logf("PAYLOAD operation=snapshot_initial_%s json_bytes=%d", member.ID, diagnosticJSON(t, payload))
				diagnosticSamples(t, "snapshot_delta_hp_"+member.ID, samples, func(i int) {
					ch := ss.CharacterInstances[cid]
					ch.ResourceCurrent["hp"] = IntegerStatValue(int64(80 + i%10))
					ss.CharacterInstances[cid] = ch
					ss.CharacterRevision++
					next, err := buildCharacterSnapshot(ss, member, cid, watcher.characterWatch, false, watcher.characterBaseline)
					if err != nil {
						t.Fatal(err)
					}
					s.sendCharacterProjection(watcher, next)
					payload = <-watcher.out
					if _, ok := payload.(characterDeltaMessage); !ok {
						t.Fatal("expected HP delta")
					}
					diagnosticJSON(t, payload)
				})
				t.Logf("PAYLOAD operation=snapshot_delta_hp_%s json_bytes=%d", member.ID, diagnosticJSON(t, payload))
				if memberIsGM(member) {
					diagnosticSamples(t, "stat_trace_on_demand", samples, func(_ int) {
						s.sendCharacterStatTrace(ss, watcher, Command{CharacterID: cid, CharacterWatch: watcher.characterWatch, CharacterSequence: watcher.characterBaseline.CharacterSequence, StatID: "s000"})
						payload = <-watcher.out
						if payload.(map[string]any)["error"] != nil {
							t.Fatal(payload)
						}
						diagnosticJSON(t, payload)
					})
					t.Logf("PAYLOAD operation=stat_trace_on_demand json_bytes=%d", diagnosticJSON(t, payload))
				}
			}
			peer := &peer{member: gm, sceneID: "scene", out: make(chan any, 1)}
			cmd := Command{SceneID: "scene", Action: &ActionExecutionRequest{CharacterID: cid, ActionID: "inspect"}}
			var targets map[string]any
			diagnosticSamples(t, "object_targets", samples, func(_ int) {
				s.sendActionTargets(ss, peer, cmd)
				targets = (<-peer.out).(map[string]any)
				if targets["error"] != nil {
					t.Fatal(targets["error"])
				}
			})
			if got := len(targets["targets"].([]actionTargetOption)); got != p.characters*p.items {
				t.Fatalf("target count %d", got)
			}
			t.Logf("PAYLOAD operation=object_targets candidates=%d json_bytes=%d", p.characters*p.items, diagnosticJSON(t, targets))
			diagnosticSamples(t, "object_targets_json", samples, func(_ int) { diagnosticJSON(t, targets) })
			region := SceneRegion{Left: 0, Top: 0, Right: 1000, Bottom: 1000}
			var sceneSnapshot any
			diagnosticSamples(t, "scene_region", samples, func(_ int) { sceneSnapshot = s.snapshotScene(ss, gm, "scene", &region) })
			t.Logf("PAYLOAD operation=scene_region json_bytes=%d", diagnosticJSON(t, sceneSnapshot))
			diagnosticSamples(t, "save_lock_held", samples, func(i int) {
				s.mu.Lock()
				ch := ss.CharacterInstances[cid]
				ch.ResourceCurrent["hp"] = IntegerStatValue(int64(90 + i%10))
				ss.CharacterInstances[cid] = ch
				s.dirty = true
				err := s.saveLocked()
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			})
			info, err := os.Stat(filepath.Join(s.root, "sessions.json"))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("PAYLOAD operation=save file_bytes=%d", info.Size())
			// One competing read per save. Measure wait separately from snapshot work.
			waits := make([]time.Duration, 0, 10)
			for i := 0; i < 10; i++ {
				s.mu.Lock()
				ready := make(chan struct{})
				done := make(chan time.Duration, 1)
				go func() {
					start := time.Now()
					close(ready)
					s.mu.Lock()
					wait := time.Since(start)
					s.mu.Unlock()
					done <- wait
				}()
				<-ready
				s.dirty = true
				err := s.saveLocked()
				s.mu.Unlock()
				waits = append(waits, <-done)
				if err != nil {
					t.Fatal(err)
				}
			}
			sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
			t.Logf("METRIC operation=competing_reader_lock_wait samples=%d p50_ms=%.3f max_ms=%.3f", len(waits), float64(waits[4])/1e6, float64(waits[9])/1e6)
		})
	}
}
