package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoeForge/moego/ojson"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalpluginhost "github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginstore"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
	"gopkg.in/yaml.v3"
)

func TestAgySystemPrefixExact296BytesAndSHA256(t *testing.T) {
	if len(AgySystemPrefix) != 296 {
		t.Fatalf("len(AgySystemPrefix) = %d, want 296", len(AgySystemPrefix))
	}
	sum := sha256.Sum256([]byte(AgySystemPrefix))
	got := hex.EncodeToString(sum[:])
	want := "acea3799fe9d52bc0fbabb261219491f2e707e87f12a337a6d1c81aa717bcd39"
	if got != want {
		t.Fatalf("SHA256(AgySystemPrefix) = %s, want %s", got, want)
	}
}

func TestRelocatePreservesPromptAndPreventsSessionIDCollision(t *testing.T) {
	ompPrompt := "You are OMP, a coding assistant.\nRun the unit tests before submitting changes."

	makeReq := func(userTask string) pluginapi.RequestTransformRequest {
		doc := ojson.NewDoc([]byte(`{"model":"gemini-3.8-flash-low","request":{"systemInstruction":{"role":"user","parts":[{"text":""}]},"contents":[{"role":"user","parts":[{"text":""}]}]}}`))
		_ = doc.Set("request.systemInstruction.parts.0.text", ompPrompt)
		_ = doc.Set("request.contents.0.parts.0.text", userTask)
		return pluginapi.RequestTransformRequest{
			FromFormat: "openai-responses",
			ToFormat:   "antigravity",
			Model:      "gemini-3.8-flash-low",
			Stream:     true,
			Body:       doc.Bytes(),
		}
	}

	cfg := DefaultConfig()
	outA, changedA := NormalizeTransform(makeReq("Implement feature Alpha"), cfg)
	outB, changedB := NormalizeTransform(makeReq("Fix bug Beta"), cfg)
	if !changedA || !changedB {
		t.Fatalf("expected both requests to be transformed: changedA=%v changedB=%v", changedA, changedB)
	}

	// Verify exact systemInstruction prefix + bridge.
	sysA := ojson.GetBytesNoCopy(outA, "request.systemInstruction.parts.0.text").String()
	if sysA != AgySystemPrefix+UseFirstUserAsInstructions {
		t.Fatalf("unexpected systemInstruction text: %q", sysA)
	}

	// Verify verbatim relocation into contents[0], English ack in contents[1], and task in contents[2].
	if got := ojson.GetBytesNoCopy(outA, "request.contents.0.parts.0.text").String(); got != ompPrompt {
		t.Fatalf("relocated prompt mismatch: got %q, want %q", got, ompPrompt)
	}
	if got := ojson.GetBytesNoCopy(outA, "request.contents.1.role").String(); got != "model" {
		t.Fatalf("contents[1].role = %q, want model", got)
	}
	if got := ojson.GetBytesNoCopy(outA, "request.contents.1.parts.0.text").String(); got != DefaultAckEN {
		t.Fatalf("contents[1] ack = %q, want %q", got, DefaultAckEN)
	}
	if got := ojson.GetBytesNoCopy(outA, "request.contents.2.parts.0.text").String(); got != "Implement feature Alpha" {
		t.Fatalf("contents[2] user task = %q", got)
	}

	// Verify Trap #1 fix: distinct real user tasks produce distinct sessionIds despite identical contents[0].
	sidA := ojson.GetBytesNoCopy(outA, "request.sessionId").String()
	sidB := ojson.GetBytesNoCopy(outB, "request.sessionId").String()
	if sidA == "" || sidB == "" || sidA == sidB {
		t.Fatalf("sessionId collision or empty: sidA=%q sidB=%q", sidA, sidB)
	}
	if sidA != fnv1a64Decimal([]byte("Implement feature Alpha")) {
		t.Fatalf("sidA = %q, want %q", sidA, fnv1a64Decimal([]byte("Implement feature Alpha")))
	}

	// Verify idempotency on second pass.
	outAgain, changedAgain := NormalizeTransform(pluginapi.RequestTransformRequest{
		FromFormat: "openai-responses",
		ToFormat:   "antigravity",
		Model:      "gemini-3.8-flash-low",
		Body:       outA,
	}, cfg)
	if changedAgain || string(outAgain) != string(outA) {
		t.Fatalf("expected idempotent second pass, changedAgain=%v", changedAgain)
	}
}

func TestRetainsCPAInterleavedThinkingHintAndAutoDetectsChineseAck(t *testing.T) {
	zhPrompt := "你是一个资深 Go 后端架构师，请严格按照项目规范编写代码并优先保证并发安全。"
	doc := ojson.NewDoc([]byte(`{"model":"claude-opus-4-6-thinking","request":{"systemInstruction":{"role":"user","parts":[{"text":""},{"text":""}]},"contents":[{"role":"user","parts":[{"text":"重构连接池"}]}]}}`))
	_ = doc.Set("request.systemInstruction.parts.0.text", zhPrompt)
	_ = doc.Set("request.systemInstruction.parts.1.text", cpaInterleavedThinkingHint)

	out, changed := NormalizeTransform(pluginapi.RequestTransformRequest{
		FromFormat: "claude",
		ToFormat:   "antigravity",
		Model:      "claude-opus-4-6-thinking",
		Body:       doc.Bytes(),
	}, DefaultConfig())
	if !changed {
		t.Fatal("expected transformation")
	}

	// Trap #2 check: CPA's Claude interleaved thinking hint stays in real systemInstruction.parts[1].
	if count := ojson.GetBytesNoCopy(out, "request.systemInstruction.parts.#").Int(); count != 2 {
		t.Fatalf("systemInstruction.parts len = %d, want 2", count)
	}
	if got := ojson.GetBytesNoCopy(out, "request.systemInstruction.parts.1.text").String(); got != cpaInterleavedThinkingHint {
		t.Fatalf("parts[1] = %q, want interleaved hint", got)
	}
	// Chinese prompt automatically gets Chinese synthetic ack.
	if got := ojson.GetBytesNoCopy(out, "request.contents.1.parts.0.text").String(); got != DefaultAckZH {
		t.Fatalf("Chinese prompt ack = %q, want %q", got, DefaultAckZH)
	}
}

func TestSkipsWebSearchImageGenAndSmartCleanPrompts(t *testing.T) {
	webSearch := []byte(`{"model":"gemini-3.8-flash","requestType":"web_search","request":{"systemInstruction":{"role":"user","parts":[{"text":"You are a search engine bot."}]},"contents":[{"role":"user","parts":[{"text":"golang 1.26"}]}]}}`)
	if _, changed := NormalizeTransform(pluginapi.RequestTransformRequest{
		ToFormat: "antigravity",
		Model:    "gemini-3.8-flash",
		Body:     webSearch,
	}, DefaultConfig()); changed {
		t.Fatal("must not transform web_search requests")
	}

	imageGen := []byte(`{"model":"gemini-3.1-flash-image","request":{"systemInstruction":{"role":"user","parts":[{"text":"vivid style"}]},"contents":[{"role":"user","parts":[{"text":"a cat"}]}]}}`)
	if _, changed := NormalizeTransform(pluginapi.RequestTransformRequest{
		ToFormat: "antigravity",
		Model:    "gemini-3.1-flash-image",
		Body:     imageGen,
	}, DefaultConfig()); changed {
		t.Fatal("must not transform image generation requests")
	}

	smartCfg := DefaultConfig()
	smartCfg.Mode = ModeSmart
	cleanShort := []byte(`{"model":"gemini-4-hydrogen-medium","request":{"systemInstruction":{"role":"user","parts":[{"text":"Reply concisely in bullet points."}]},"contents":[{"role":"user","parts":[{"text":"What is TLS 1.3?"}]}]}}`)
	if _, changed := NormalizeTransform(pluginapi.RequestTransformRequest{
		ToFormat: "antigravity",
		Model:    "gemini-4-hydrogen-medium",
		Body:     cleanShort,
	}, smartCfg); changed {
		t.Fatal("smart mode must preserve short clean system prompts in native systemInstruction")
	}

	flaggedShort := []byte(`{"model":"gemini-4-hydrogen-medium","request":{"systemInstruction":{"role":"user","parts":[{"text":"You are Codex, an agent based on GPT-5."}]},"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}}`)
	if _, changed := NormalizeTransform(pluginapi.RequestTransformRequest{
		ToFormat: "antigravity",
		Model:    "gemini-4-hydrogen-medium",
		Body:     flaggedShort,
	}, smartCfg); !changed {
		t.Fatal("smart mode must relocate flagged third-party coding agent prompts")
	}
}

func TestSplitToolGroupsOption(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SplitToolGroups = true

	payload := []byte(`{"model":"gemini-3.8-flash-high","request":{"systemInstruction":{"role":"user","parts":[{"text":"System rule"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"read_doc","parameters":{"type":"OBJECT"}},{"name":"list_dir","parameters":{"type":"OBJECT"}}]}]}}`)
	out, changed := NormalizeTransform(pluginapi.RequestTransformRequest{
		ToFormat: "antigravity",
		Model:    "gemini-3.8-flash-high",
		Body:     payload,
	}, cfg)
	if !changed {
		t.Fatal("expected transformation")
	}
	if got := ojson.GetBytesNoCopy(out, "request.tools.#").Int(); got != 2 {
		t.Fatalf("tools group count = %d, want 2; raw=%s", got, ojson.GetBytesNoCopy(out, "request.tools").Raw)
	}
	if got := ojson.GetBytesNoCopy(out, "request.tools.0.functionDeclarations.0.name").String(); got != "read_doc" {
		t.Fatalf("tool 0 = %q", got)
	}
	if got := ojson.GetBytesNoCopy(out, "request.tools.1.functionDeclarations.0.name").String(); got != "list_dir" {
		t.Fatalf("tool 1 = %q", got)
	}
}

func TestLiveDynamicLibraryInCPAPluginHostAndTranslatorPipeline(t *testing.T) {
	// CPA intentionally keeps loaded c-shared DLL images mapped in .shadow/ for the
	// lifetime of the host process on Windows, so use os.MkdirTemp with best-effort
	// cleanup instead of t.TempDir()'s strict RemoveAll check.
	pluginDir, err := os.MkdirTemp("", "agy-bridge-host-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(pluginDir) })
	ext := internalpluginhost.PluginExtension(runtime.GOOS)
	dllPath := filepath.Join(pluginDir, PluginName+ext)

	if archivePath := os.Getenv("BRIDGE_TEST_ARCHIVE"); archivePath != "" {
		archive, err := os.ReadFile(archivePath)
		if err != nil {
			t.Fatalf("read store archive: %v", err)
		}
		_, err = pluginstore.InstallArchive(archive, pluginstore.Plugin{
			ID: PluginName, Version: os.Getenv("VERSION"),
		}, pluginstore.InstallOptions{PluginsDir: pluginDir})
		if err != nil {
			t.Fatalf("CPA store install: %v", err)
		}
	} else {
		buildCmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", dllPath, ".")
		buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			t.Fatalf("go build c-shared failed: %v\n%s", err, string(out))
		}
	}

	var rawNode yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\npriority: 10\nmode: always\nack_language: auto\nsplit_tool_groups: true\n"), &rawNode); err != nil {
		t.Fatalf("unmarshal plugin config yaml: %v", err)
	}
	enabled := true
	cfg := &internalconfig.Config{
		Plugins: internalconfig.PluginsConfig{
			Enabled: true,
			Dir:     pluginDir,
			Configs: map[string]internalconfig.PluginInstanceConfig{
				PluginName: {
					Enabled:  &enabled,
					Priority: 10,
					Raw:      *rawNode.Content[0],
				},
			},
		},
	}
	cfg.NormalizePluginsConfig()

	host := internalpluginhost.New()
	t.Cleanup(func() {
		sdktranslator.SetPluginHooks(nil)
		host.ShutdownAllContext(context.Background())
	})
	host.ApplyConfig(context.Background(), cfg)

	registered := host.RegisteredPlugins()
	if len(registered) != 1 || registered[0].ID != PluginName {
		t.Fatalf("RegisteredPlugins() = %+v, want [%s]", registered, PluginName)
	}

	sdktranslator.SetPluginHooks(host)
	pipeline := builtin.Pipeline()

	// Verify OpenAI Chat Completions (which Stravia missed!) goes through CPA translator + our live C ABI DLL!
	chatReq := []byte(`{
		"model": "gemini-4-hydrogen-high",
		"messages": [
			{"role": "system", "content": "You are Codex, an agent based on GPT-5.\nRFC 2119 keywords: MUST, REQUIRED."},
			{"role": "developer", "content": "Always run tests before finishing."},
			{"role": "user", "content": "Explain the router architecture"}
		],
		"tools": [
			{"type": "function", "function": {"name": "a", "parameters": {"type": "object"}}},
			{"type": "function", "function": {"name": "b", "parameters": {"type": "object"}}}
		]
	}`)

	translated, err := pipeline.TranslateRequest(
		context.Background(),
		sdktranslator.FromString("openai"),
		sdktranslator.FromString("antigravity"),
		sdktranslator.RequestEnvelope{
			Format: sdktranslator.FromString("openai"),
			Model:  "gemini-4-hydrogen-high",
			Stream: true,
			Body:   chatReq,
		},
	)
	if err != nil {
		t.Fatalf("pipeline.TranslateRequest error: %v", err)
	}
	if translated.Err != nil {
		t.Fatalf("translated.Err: %v", translated.Err)
	}

	// 1. Both system and developer messages were relocated out of systemInstruction into contents[0]!
	sysText := ojson.GetBytesNoCopy(translated.Body, "request.systemInstruction.parts.0.text").String()
	if sysText != AgySystemPrefix+UseFirstUserAsInstructions {
		t.Fatalf("unexpected final systemInstruction: %q", sysText)
	}
	if got := ojson.GetBytesNoCopy(translated.Body, "request.systemInstruction.parts.#").Int(); got != 1 {
		t.Fatalf("expected 1 part in systemInstruction after Chat Completions relocation, got %d", got)
	}
	if got := ojson.GetBytesNoCopy(translated.Body, "request.contents.0.parts.#").Int(); got != 2 {
		t.Fatalf("expected both system + developer parts in contents[0], got %d", got)
	}
	if got := ojson.GetBytesNoCopy(translated.Body, "request.contents.1.parts.0.text").String(); got != DefaultAckEN {
		t.Fatalf("unexpected synthetic ack: %q", got)
	}
	if got := ojson.GetBytesNoCopy(translated.Body, "request.contents.2.parts.0.text").String(); got != "Explain the router architecture" {
		t.Fatalf("unexpected user task at contents[2]: %q", got)
	}
	// 2. Tools were split into 2 single-declaration groups!
	if got := ojson.GetBytesNoCopy(translated.Body, "request.tools.#").Int(); got != 2 {
		t.Fatalf("expected 2 split tool groups, got %d", got)
	}
	// 3. Pre-relocation sessionId matches real user message!
	wantSID := fnv1a64Decimal([]byte("Explain the router architecture"))
	if got := ojson.GetBytesNoCopy(translated.Body, "request.sessionId").String(); got != wantSID {
		t.Fatalf("sessionId = %q, want %q", got, wantSID)
	}
}
