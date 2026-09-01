// Package main implements the greeter test plugin for WU2 end-to-end verification.
// Build with:
//
//	GOOS=wasip1 GOARCH=wasm go build -o greeter.wasm .
//
// This plugin exercises the forge WASM ABI via //go:wasmexport and //go:wasmimport.
// It exposes one tool "greeter_greet" with permission "fs.read" that round-trips
// through the host's fs_read import before returning a greeting.
//
// The allocator is a simple bump over Go heap slices; each forge_alloc call
// allocates a new Go slice whose backing memory lives in the same linear memory
// visible to the host via wazero. This keeps the plugin self-contained without
// a manual free.
//
//go:build wasip1

package main

import (
	"encoding/json"
	"unsafe"
)

// Host imports (module "forge_host").
//
//go:wasmimport forge_host log
func hostLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport forge_host fs_read
func hostFsRead(pathPtr, pathLen uint32) uint64

//go:wasmimport forge_host fs_write
func hostFsWrite(pathPtr, pathLen, dataPtr, dataLen uint32) uint32

//go:wasmimport forge_host shell_exec
func hostShellExec(cmdPtr, cmdLen, argsJSONPtr, argsJSONLen uint32) uint64

//go:wasmimport forge_host git_run
func hostGitRun(argsJSONPtr, argsJSONLen uint32) uint64

//go:wasmimport forge_host net_fetch
func hostNetFetch(urlPtr, urlLen uint32) uint64

// pack packs ptr/len into i64.
func pack(ptr uint32, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }

// unpack splits packed i64.
func unpack(packed uint64) (uint32, uint32) { return uint32(packed >> 32), uint32(packed & 0xffffffff) }

// readString decodes a string from the plugin's own linear memory given ptr/len.
func readString(ptr uint32, length uint32) string {
	if length == 0 {
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length))
}

// allocAndWrite allocates plugin memory via make and returns packed ptr:len.
// The allocated slice's backing array lives in wasm linear memory and is visible to the host.
func allocAndWrite(data []byte) uint64 {
	if len(data) == 0 {
		return pack(0, 0)
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	ptr := uint32(uintptr(unsafe.Pointer(&buf[0])))
	// Keep buf alive until exported function returns (escape to heap keeps it).
	// The memory remains valid for the host to read immediately after return.
	return pack(ptr, uint32(len(data)))
}

// forge_alloc is called by the host to reserve response buffers. We implement it via the
// same make-slice trick so the returned pointer is host-readable.

//go:wasmexport forge_alloc
func forgeAlloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	buf := make([]byte, size)
	return uint32(uintptr(unsafe.Pointer(&buf[0])))
}

// forge_abi_version reports the plugin ABI version.

//go:wasmexport forge_abi_version
func forgeAbiVersion() uint32 { return 1 }

// forge_tool_list returns JSON array of ToolExport.

//go:wasmexport forge_tool_list
func forgeToolList() uint64 {
	list := []map[string]string{
		{"name": "greeter_greet", "description": "Greets a user and echoes a file via host fs_read", "permission": "fs.read"},
	}
	data, _ := json.Marshal(list)
	return allocAndWrite(data)
}

// forge_tool_invoke dispatches tool calls. Expected args JSON: {"name":"world","file":"/path/to/data.txt"}
// It calls hostFsRead on "file" when provided, then returns greeting JSON.

//go:wasmexport forge_tool_invoke
func forgeToolInvoke(fnPtr, fnLen, argsPtr, argsLen uint32) uint64 {
	fnName := readString(fnPtr, fnLen)
	argsRaw := readString(argsPtr, argsLen)

	if fnName != "greeter_greet" {
		msg, _ := json.Marshal(map[string]string{"error": "unknown tool: " + fnName})
		return allocAndWrite(msg)
	}

	var args map[string]any
	if len(argsRaw) > 0 {
		_ = json.Unmarshal([]byte(argsRaw), &args)
	}
	if args == nil {
		args = map[string]any{}
	}
	name, _ := args["name"].(string)
	if name == "" {
		name = "world"
	}
	filePath, _ := args["file"].(string)

	var fileContent string
	if filePath != "" {
		packed := hostFsRead(uint32(uintptr(unsafe.Pointer(&[]byte(filePath)[0]))), uint32(len(filePath)))
		fp, fl := unpack(packed)
		if fl > 0 {
			raw := readString(fp, fl)
			// Host returns JSON-quoted string or error envelope.
			var decoded string
			if json.Unmarshal([]byte(raw), &decoded) == nil {
				fileContent = decoded
			} else {
				// Check error envelope.
				var env map[string]string
				if json.Unmarshal([]byte(raw), &env) == nil {
					if e, ok := env["error"]; ok {
						fileContent = "error:" + e
					} else {
						fileContent = raw
					}
				} else {
					fileContent = raw
				}
			}
		}
	}
	greeting := "hello " + name
	if fileContent != "" {
		greeting += " file:" + fileContent
	}
	// Return greeting envelope: {"greeting":"..."} to exercise JSON object handling,
	// but the host tool bridge also accepts plain string.
	// Return as JSON object for richer test.
	out, _ := json.Marshal(map[string]string{"greeting": greeting})
	return allocAndWrite(out)
}

func main() {}
