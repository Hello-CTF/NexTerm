//go:build smoke

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type desktopSmokeResult struct {
	Ready     string `json:"ready"`
	Transport string `json:"transport"`
	Platform  string `json:"platform"`
	Body      string `json:"body"`
	BinaryOK  bool   `json:"binaryOK"`
	ReopenOK  bool   `json:"reopenOK"`
	JSONOK    bool   `json:"jsonOK"`
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
}

var desktopSmokeModules = []core.Module{{
	Name: "desktop-smoke",
	RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
		return ipc.Register(dispatcher, "desktop_smoke_json", func(ctx context.Context, call *ipc.Call, _ struct{}) (any, error) {
			stream, err := call.Streams.OpenJSON(ctx, call.Channel)
			if err != nil {
				return nil, err
			}
			defer stream.Close()
			if err := stream.SendJSON(ctx, json.RawMessage(`{"kind":"delta","sequence":9007199254740993}`)); err != nil {
				return nil, err
			}
			return nil, stream.SendJSON(ctx, json.RawMessage(`{"kind":"done"}`))
		})
	},
}}

func desktopSmokeEnabled() bool { return true }

var desktopSmokeResults = make(chan desktopSmokeResult, 1)

var desktopSmokeStart = time.Now()

func desktopSmokeElapsed() time.Duration {
	return time.Since(desktopSmokeStart).Round(100 * time.Millisecond)
}

func dumpDesktopSmokeGoroutines() {
	fmt.Fprintf(os.Stderr, "desktop smoke: goroutine dump at t=%s\n", desktopSmokeElapsed())
	if profile := pprof.Lookup("goroutine"); profile != nil {
		_ = profile.WriteTo(os.Stderr, 2)
	}
}

func runDesktopSmoke(wailsApp *application.App, window *application.WebviewWindow) int {
	results := desktopSmokeResults
	wailsApp.Event.On("nexterm-smoke-result", func(event *application.CustomEvent) {
		raw, err := json.Marshal(event.Data)
		if err != nil {
			return
		}
		var result desktopSmokeResult
		if err := json.Unmarshal(raw, &result); err == nil {
			select {
			case results <- result:
			default:
			}
		}
	})
	delivered := make(chan struct{})
	go func() {
		time.Sleep(1500 * time.Millisecond)
		for {
			select {
			case <-delivered:
				return
			default:
				fmt.Fprintf(os.Stderr, "desktop smoke: injecting script t=%s\n", desktopSmokeElapsed())
				window.ExecJS(desktopSmokeScript)
				fmt.Fprintf(os.Stderr, "desktop smoke: injection returned t=%s\n", desktopSmokeElapsed())
			}
			select {
			case <-delivered:
				return
			case <-time.After(2500 * time.Millisecond):
			}
		}
	}()
	go func() {
		select {
		case result := <-results:
			close(delivered)
			fmt.Printf("desktop smoke: %+v\n", result)
			if result.OK && result.Ready == "complete" && result.Transport == "desktop" && result.Platform == runtime.GOOS && result.Body != "" && result.BinaryOK && result.ReopenOK && result.JSONOK && result.Error == "" {
				os.Exit(0)
			}
			os.Exit(1)
		case <-time.After(60 * time.Second):
			close(delivered)
			fmt.Fprintln(os.Stderr, "desktop smoke: timed out")
			dumpDesktopSmokeGoroutines()
			os.Exit(1)
		}
	}()
	if err := wailsApp.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "desktop smoke: application run:", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "desktop smoke: application stopped before result")
	return 1
}

const desktopSmokeScript = `(async () => {
  if (window.__NEXTERM_SMOKE_ACTIVE__) return;
  window.__NEXTERM_SMOKE_ACTIVE__ = true;
  const result = { ready: "", transport: "", platform: "", body: "", binaryOK: false, reopenOK: false, jsonOK: false, ok: false, error: "" };
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const progress = async (step) => {
    try {
      await fetch("/__desktop_smoke_progress__", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ step }) });
    } catch (_) {}
  };
  await progress("script-start");
  // Wait for the real app document: the desktop transport marker is injected
  // into <head>, and the Go-side verdict requires readyState "complete".
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline && (document.readyState !== "complete" || window.__NEXTERM_TRANSPORT__ !== "desktop")) {
    await wait(100);
  }
  result.ready = document.readyState;
  result.transport = window.__NEXTERM_TRANSPORT__;
  await progress("document-ready:" + document.readyState + ":" + String(window.__NEXTERM_TRANSPORT__));
  window._wails = window._wails || {};
  const listeners = new Map();
  const nativeDispatch = window._wails.dispatchWailsEvent;
  window._wails.dispatchWailsEvent = (event) => {
    const callbacks = listeners.get(event.name);
    if (callbacks) callbacks.forEach((callback) => callback(event));
    if (nativeDispatch) return nativeDispatch.call(window._wails, event);
  };
  const on = (name, callback) => {
    const callbacks = listeners.get(name) || [];
    callbacks.push(callback);
    listeners.set(name, callbacks);
    return () => listeners.delete(name);
  };
  let callSequence = 0;
  const call = async (cmd, args, envelope = {}) => {
    const request = { cmd, args: args === undefined ? null : args, ...envelope };
    const response = await fetch("/wails/runtime", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ object: 0, method: 0, args: { "call-id": "desktop-smoke-" + (++callSequence), methodName: "main.Service.Call", args: [request] } }),
    });
    const payload = await response.json();
    if (!response.ok) throw payload && payload.message ? new Error(payload.message) : new Error(cmd + " transport failed");
    if (!payload || payload.ok !== true) throw new Error(cmd + " failed: " + JSON.stringify(payload && payload.error ? payload.error : payload));
    return payload.data;
  };
  const bytes = (text) => Array.from(new TextEncoder().encode(text));
  const text = (frame) => new TextDecoder().decode(new Uint8Array(frame));
  try {
    result.body = document.body ? document.body.innerText.slice(0, 500) : "";
    result.platform = await call("app_platform");
    await progress("app_platform:" + result.platform);
    const connected = await call("session_connect_local");
    await progress("session_connect_local:" + connected.id);
    const channelID = "desktop-smoke-" + Date.now();
    const frames = [];
    const off = on("channel://" + channelID, (event) => frames.push(event.data));
    const tabID = await call("terminal_attach_smoke", { sessionId: connected.id, cols: 80, rows: 24 }, { channel: channelID, clientId: "desktop-smoke" });
    await progress("terminal_attach_smoke:" + tabID);
    await wait(200);
    result.body = JSON.stringify(await call("terminal_list"));
    await progress("terminal_list");
    await call("terminal_write", { args: { tabId: tabID, data: bytes("printf 'nexterm-smoke-ok\\n'\r"), clientId: "desktop-smoke" } });
    await progress("terminal_write");
    await wait(1500);
    result.binaryOK = frames.some((frame) => Array.isArray(frame) && text(frame).includes("nexterm-smoke-ok"));
    await progress("binaryOK:" + result.binaryOK);
    off();
    await call("terminal_detach", { tabId: tabID, channelId: channelID });
    await progress("terminal_detach");
    const replay = [];
    const offReplay = on("channel://" + channelID, (event) => replay.push(event.data));
    await call("terminal_attach_tab", { tabId: tabID, replayBytes: 65536 }, { channel: channelID, clientId: "desktop-smoke" });
    await progress("terminal_attach_tab");
    await wait(200);
    result.reopenOK = replay.some((frame) => Array.isArray(frame) && text(frame).includes("nexterm-smoke-ok"));
    await progress("reopenOK:" + result.reopenOK);
    offReplay();
    const jsonFrames = [];
    const jsonChannel = "desktop-smoke-json-" + Date.now();
    const offJSON = on("channel://" + jsonChannel, (event) => jsonFrames.push(event.data));
    await call("desktop_smoke_json", null, { channel: jsonChannel });
    await progress("desktop_smoke_json");
    await wait(100);
    result.jsonOK = jsonFrames.length === 2 && jsonFrames[0].kind === "delta" && jsonFrames[1].kind === "done";
    await progress("jsonOK:" + result.jsonOK);
    offJSON();
    await call("terminal_close_tab", { tabId: tabID, clientId: "desktop-smoke" });
    await progress("terminal_close_tab");
    await call("session_disconnect", { sessionId: connected.id });
    await progress("session_disconnect");
    result.ok = result.binaryOK && result.reopenOK && result.jsonOK;
  } catch (error) {
    result.error = String(error && error.message ? error.message : error);
    result.ok = false;
    await progress("error:" + result.error);
  }
  await progress("posting-result");
  try {
    await fetch("/__desktop_smoke_result__", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(result) });
  } catch (_) {
    if (window._wails && window._wails.dispatchWailsEvent) {
      window._wails.dispatchWailsEvent({ name: "nexterm-smoke-result", data: result });
    } else if (window.wails && window.wails.Events) {
      window.wails.Events.Emit("nexterm-smoke-result", result);
    }
  }
})();`
