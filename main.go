package main

/*
#include <limits.h>
#include <stdlib.h>
#include "native.h"
*/
import "C"

import (
	"sync/atomic"
	"unsafe"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var activeConfig atomic.Pointer[Config]

func init() {
	cfg := DefaultConfig()
	activeConfig.Store(&cfg)
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	RequestNormalizer bool `json:"request_normalizer"`
}

func main() {}

//export cliproxyPluginInit
func cliproxyPluginInit(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || host.abi_version != C.uint32_t(pluginabi.ABIVersion) || plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	// C.GoBytes takes a C.int, while the ABI uses size_t. Reject lengths
	// that would truncate before converting or reading from the pointer.
	if requestLen > C.size_t(C.INT_MAX) || (request == nil && requestLen != 0) {
		writeResponse(response, errorEnvelope("invalid_request", "invalid request buffer"))
		return 1
	}
	var requestBytes []byte
	if requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodRequestNormalize:
		return handleNormalizeRequest(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg, err := DecodeConfig(req.ConfigYAML)
	if err != nil {
		return err
	}
	activeConfig.Store(&cfg)
	return nil
}

func currentConfig() Config {
	if ptr := activeConfig.Load(); ptr != nil {
		return *ptr
	}
	return DefaultConfig()
}

func handleNormalizeRequest(raw []byte) ([]byte, error) {
	var req pluginapi.RequestTransformRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	updated, _ := NormalizeTransform(req, currentConfig())
	return okEnvelope(pluginapi.PayloadResponse{Body: updated})
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             PluginName,
			Version:          pluginVersion,
			Author:           "ukiyo-dev",
			GitHubRepository: "https://github.com/ukiyo-dev/antigravity-prompt-bridge",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "mode",
					Type:        pluginapi.ConfigFieldTypeEnum,
					Default:     ModeAlways,
					EnumValues:  []string{ModeAlways, ModeSmart},
					Description: "Relocation policy: 'always' relocates every non-empty Agent systemInstruction; 'smart' only relocates long prompts or known coding-agent prompts.",
				},
				{
					Name:        "ack_language",
					Type:        pluginapi.ConfigFieldTypeEnum,
					Default:     AckLangAuto,
					EnumValues:  []string{AckLangAuto, AckLangEN, AckLangZH},
					Description: "Language of the synthetic model acknowledgment turn ('auto' detects from prompt text).",
				},
				{
					Name:        "custom_ack_text",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "Optional custom synthetic model acknowledgment text overriding ack_language.",
				},
				{
					Name:        "smart_min_bytes",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Default:     defaultSmartMinBytes,
					Description: "Byte length threshold triggering relocation when mode is 'smart'.",
				},
				{
					Name:        "prefix_when_empty",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     false,
					Description: "Inject the 296-byte Antigravity <identity> prefix even when request has no systemInstruction.",
				},
				{
					Name:        "split_tool_groups",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     false,
					Description: "Split multi-function declarations into one functionDeclarations entry per tool group (CLI 1.5.11+ layout).",
				},
			},
		},
		Capabilities: registrationCapability{
			RequestNormalizer: true,
		},
	}
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
