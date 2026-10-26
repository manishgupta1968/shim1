package ondc_e2e_test

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"text/template"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
)

// pluginsToBuild are the plugins the end-to-end ONIX processes load. Each entry is the plugin id, which is
// both the .so name and the id used in the YAML, and its directory under pkg/plugin/implementation.
var pluginsToBuild = []string{
	"cache", "registry", "simplekeymanager", "router", "signer", "signvalidator", "schemavalidator", "reqpreprocessor",
}

// participant is a network participant: its id, its registered key id and its signing key pair.
type participant struct {
	ID    string
	KeyID string
	Priv  ed25519.PrivateKey
	Pub   ed25519.PublicKey
	Encr  []byte // not used for signing; simplekeymanager only requires the key to be present
}

func newParticipant(id string) participant {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	encr := make([]byte, 32)
	if _, err := rand.Read(encr); err != nil {
		panic(err)
	}
	return participant{ID: id, KeyID: uuid.NewString(), Priv: priv, Pub: pub, Encr: encr}
}

func (p participant) seedB64() string { return base64.StdEncoding.EncodeToString(p.Priv.Seed()) }
func (p participant) pubB64() string  { return base64.StdEncoding.EncodeToString(p.Pub) }
func (p participant) encrB64() string { return base64.StdEncoding.EncodeToString(p.Encr) }

// sharedInfra is everything the tests share: built binaries, Redis, the registry and one world per ONDC domain.
type sharedInfra struct {
	root      string
	binDir    string
	pluginDir string
	schemaDir string
	redis     *miniredis.Miniredis
	registry  *registry
	worlds    []*world

	// Participants used by the negative tests. rogue is a valid subscriber that signs messages it should not;
	// expired and unsubscribed are registered, but their keys are no longer usable.
	rogue, expired, unsubscribed participant
}

func (i *sharedInfra) close() {
	for _, w := range i.worlds {
		w.close()
	}
	if i.registry != nil {
		i.registry.close()
	}
	if i.redis != nil {
		i.redis.Close()
	}
}

func setupInfra(root string) (*sharedInfra, error) {
	i := &sharedInfra{root: root}

	if dir := os.Getenv("ONIX_E2E_BIN_DIR"); dir != "" {
		i.binDir = dir
		fmt.Printf("using prebuilt binaries from %s (ONIX_E2E_BIN_DIR)\n", dir)
	} else {
		i.binDir = filepath.Join(root, "bin")
		if err := buildAll(i.binDir); err != nil {
			return i, err
		}
	}
	i.pluginDir = filepath.Join(i.binDir, "plugins")

	var err error
	i.schemaDir, err = extractSchemas(filepath.Join(root, "schemas"))
	if err != nil {
		return i, err
	}

	i.redis, err = miniredis.Run()
	if err != nil {
		return i, fmt.Errorf("start miniredis: %w", err)
	}
	i.registry = newRegistry()
	i.rogue = newParticipant("rogue.example.com")
	i.expired = newParticipant("expired.example.com")
	i.unsubscribed = newParticipant("unsubscribed.example.com")
	i.registry.add(i.rogue, "BAP", "SUBSCRIBED")
	i.registry.add(i.expired, "BAP", "EXPIRED")
	i.registry.add(i.unsubscribed, "BAP", "UNSUBSCRIBED")

	for _, spec := range domains {
		w, err := startWorld(i, spec)
		if w != nil {
			i.worlds = append(i.worlds, w)
		}
		if err != nil {
			return i, fmt.Errorf("start %s world: %w", spec.name, err)
		}
	}
	return i, nil
}

// findModuleRoot walks up from the working directory to the directory holding go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above the working directory")
		}
		dir = parent
	}
}

// buildAll builds the server and the plugins the same way install/build-plugins.sh does, in parallel.
func buildAll(binDir string) error {
	root, err := findModuleRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(binDir, "plugins"), 0o755); err != nil {
		return err
	}
	fmt.Printf("=== building server and %d plugins (first run takes a few minutes) ===\n", len(pluginsToBuild))
	start := time.Now()

	type job struct {
		name string
		args []string
	}
	jobs := []job{{"server", []string{"build", "-o", filepath.Join(binDir, "server"), "./cmd/adapter"}}}
	for _, id := range pluginsToBuild {
		jobs = append(jobs, job{id + ".so", []string{
			"build", "-buildmode=plugin", "-o", filepath.Join(binDir, "plugins", id+".so"),
			"./pkg/plugin/implementation/" + id + "/cmd/plugin.go",
		}})
	}
	errs := make(chan error, len(jobs))
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			cmd := exec.Command("go", j.args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("build %s: %v\n%s", j.name, err, out)
			}
		}(j)
	}
	wg.Wait()
	close(errs)
	var msgs []string
	for e := range errs {
		msgs = append(msgs, e.Error())
	}
	if len(msgs) > 0 {
		return fmt.Errorf("%s", strings.Join(msgs, "\n"))
	}
	fmt.Printf("=== build finished in %s ===\n", time.Since(start).Round(time.Second))
	return nil
}

var (
	schemasOnce sync.Once
	schemasDir  string
	schemasErr  error
)

// extractedSchemas returns a directory holding the schemas ONIX ships in schemas.zip. It does not need the
// running world, so the fixture self-check can use it on its own.
func extractedSchemas(t testing.TB) string {
	t.Helper()
	schemasOnce.Do(func() {
		schemasDir, schemasErr = extractSchemas(filepath.Join(tmpRoot, "schemas-selfcheck"))
	})
	if schemasErr != nil {
		t.Fatalf("extract schemas.zip: %v", schemasErr)
	}
	return schemasDir
}

// extractSchemas unzips <module root>/schemas.zip into dir/schemas, skipping the macOS metadata, and
// returns the schema directory ONIX's schemavalidator plugin reads.
func extractSchemas(dir string) (string, error) {
	root, err := findModuleRoot()
	if err != nil {
		return "", err
	}
	zr, err := zip.OpenReader(filepath.Join(root, "schemas.zip"))
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "__MACOSX/") || strings.HasSuffix(f.Name, ".DS_Store") {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) {
			return "", fmt.Errorf("unsafe path in schemas.zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "schemas"), nil
}

// ---------------------------------------------------------------------------
// ONIX processes
// ---------------------------------------------------------------------------

type process struct {
	name    string
	cmd     *exec.Cmd
	logPath string
	exited  chan struct{}
}

// startServer starts one ONIX process and waits until its /health endpoint answers.
func startServer(i *sharedInfra, name, cfgPath, logPath string, port int) (*process, error) {
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(i.binDir, "server"), "--config="+cfgPath)
	cmd.Dir = i.root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	p := &process{name: name, cmd: cmd, logPath: logPath, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		close(p.exited)
	}()

	deadline := time.Now().Add(40 * time.Second)
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			return p, fmt.Errorf("%s exited during startup; log tail:\n%s", name, p.tail(40))
		default:
		}
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return p, fmt.Errorf("%s did not become healthy in time; log tail:\n%s", name, p.tail(40))
}

func (p *process) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	select {
	case <-p.exited:
		return
	default:
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.exited
	}
}

// tail returns the last n lines of the process log.
func (p *process) tail(n int) string {
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", p.logPath, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// freePort asks the OS for a free TCP port. There is a small window before the process binds it; that is
// acceptable for tests.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// renderTemplate renders a text/template file from testdata/config into dst.
func renderTemplate(src, dst string, data any) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tpl, err := template.New(filepath.Base(src)).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render %s: %w", src, err)
	}
	return os.WriteFile(dst, buf.Bytes(), 0o644)
}
