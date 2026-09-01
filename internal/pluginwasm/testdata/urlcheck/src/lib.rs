// Dogfood urlcheck plugin — verifies WU6 wizard output and real net_fetch.
// ABIVersion = 1 (FROZEN). Dependency-free Rust (std only), no crates.
// Tool: urlcheck_status — takes {"url":"..."}, calls forge_host.net_fetch,
// returns {"url":..., "status":..., "bytes": len(body)} or {"error":"..."} envelope.
//
// Limitations documented:
//   - Minimal JSON extraction: finds the first occurrence of the key string "\"url\""
//     followed by ':' and a quoted string value; does not handle escapes or nested
//     structures. Bounded and sufficient for the WU6 test harness.
//   - Response parsing similarly scans for "\"status\"" and "\"body\"" substrings.

#![allow(static_mut_refs)]

static mut HEAP: [u8; 4194304] = [0; 4194304];
static mut HEAP_POS: usize = 0;

#[link(wasm_import_module = "forge_host")]
extern "C" {
    fn log(level_ptr: i32, level_len: i32, msg_ptr: i32, msg_len: i32);
    fn fs_read(path_ptr: i32, path_len: i32) -> i64;
    fn fs_write(path_ptr: i32, path_len: i32, data_ptr: i32, data_len: i32) -> i32;
    fn shell_exec(cmd_ptr: i32, cmd_len: i32, args_ptr: i32, args_len: i32) -> i64;
    fn git_run(args_ptr: i32, args_len: i32) -> i64;
    fn net_fetch(url_ptr: i32, url_len: i32) -> i64;
}

#[no_mangle]
pub extern "C" fn forge_abi_version() -> i32 {
    1
}

#[no_mangle]
pub extern "C" fn forge_alloc(size: i32) -> i32 {
    unsafe {
        let pos = HEAP_POS;
        HEAP_POS += size as usize;
        if HEAP_POS > HEAP.len() {
            return 0;
        }
        HEAP.as_mut_ptr().add(pos) as i32
    }
}

fn pack(ptr: i32, len: i32) -> i64 {
    ((ptr as i64) << 32) | (len as i64 & 0xffffffff)
}

fn unpack(packed: i64) -> (i32, i32) {
    ((packed >> 32) as i32, (packed & 0xffffffff) as i32)
}

// Allocate and copy a Rust &str into wasm linear memory, return pack.
fn alloc_str(s: &str) -> i64 {
    let bytes = s.as_bytes();
    let ptr = forge_alloc(bytes.len() as i32);
    if ptr == 0 {
        return 0;
    }
    unsafe {
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), HEAP.as_mut_ptr().add(HEAP_POS - bytes.len()) as *mut u8, bytes.len());
    }
    pack(ptr, bytes.len() as i32)
}

fn alloc_bytes(b: &[u8]) -> i64 {
    let ptr = forge_alloc(b.len() as i32);
    if ptr == 0 {
        return 0;
    }
    unsafe {
        std::ptr::copy_nonoverlapping(b.as_ptr(), HEAP.as_mut_ptr().add(HEAP_POS - b.len()) as *mut u8, b.len());
    }
    pack(ptr, b.len() as i32)
}

fn error_envelope(msg: &str) -> i64 {
    // Escape quotes naively for this bounded use (msg is ASCII, no embedded quotes in our errors).
    let json = format!(r#"{{"error":"{}"}}"#, msg.replace('"', "'"));
    alloc_str(&json)
}

// Read a string from wasm memory given ptr/len (unsafe: caller guarantees validity).
unsafe fn read_str(ptr: i32, len: i32) -> String {
    if len <= 0 {
        return String::new();
    }
    let slice = std::slice::from_raw_parts(ptr as *const u8, len as usize);
    // Lossy for binary safety; test bodies are UTF-8.
    String::from_utf8_lossy(slice).to_string()
}

// Naive substring search in haystack bytes.
fn find_substring(hay: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.len() > hay.len() {
        return None;
    }
    for i in 0..=hay.len() - needle.len() {
        if &hay[i..i + needle.len()] == needle {
            return Some(i);
        }
    }
    None
}

// Extract first JSON string value for a given key (e.g., "\"url\"").
// Returns the raw string content without quotes, if found.
fn extract_json_string(hay: &[u8], key: &[u8]) -> Option<String> {
    let pos = find_substring(hay, key)?;
    let mut i = pos + key.len();
    // Find ':' after key.
    while i < hay.len() && hay[i] != b':' {
        i += 1;
    }
    if i >= hay.len() {
        return None;
    }
    i += 1; // skip ':'
           // Skip whitespace.
    while i < hay.len() && (hay[i] == b' ' || hay[i] == b'\t' || hay[i] == b'\n' || hay[i] == b'\r') {
        i += 1;
    }
    if i >= hay.len() || hay[i] != b'"' {
        return None;
    }
    i += 1;
    let start = i;
    while i < hay.len() && hay[i] != b'"' {
        // Minimal handling: skip escaped quotes \" by advancing one extra.
        if hay[i] == b'\\' && i + 1 < hay.len() {
            i += 2;
            continue;
        }
        i += 1;
    }
    if i >= hay.len() {
        return None;
    }
    let end = i;
    let raw = &hay[start..end];
    // Unescape minimal: turn \" into " and \\ into \.
    // For url values (no escapes expected in tests), just return as is.
    Some(String::from_utf8_lossy(raw).to_string())
}

fn parse_status(hay: &[u8]) -> Option<i32> {
    let pos = find_substring(hay, b"\"status\"")?;
    let mut i = pos + 8;
    while i < hay.len() && hay[i] != b':' {
        i += 1;
    }
    if i >= hay.len() {
        return None;
    }
    i += 1;
    while i < hay.len() && (hay[i] == b' ' || hay[i] == b'\t') {
        i += 1;
    }
    let start = i;
    // Optional minus.
    if i < hay.len() && hay[i] == b'-' {
        i += 1;
    }
    while i < hay.len() && hay[i].is_ascii_digit() {
        i += 1;
    }
    if start == i {
        return None;
    }
    let num_str = std::str::from_utf8(&hay[start..i]).ok()?;
    num_str.parse::<i32>().ok()
}

fn body_len_from_response(hay: &[u8]) -> usize {
    // Find "body" key and extract its string value length (decoded).
    // For test, body is plain text without escapes, so raw len equals decoded len.
    if let Some(s) = extract_json_string(hay, b"\"body\"") {
        return s.len();
    }
    0
}

#[no_mangle]
pub extern "C" fn forge_tool_list() -> i64 {
    let json = r#"[{"name":"urlcheck_status","description":"Checks URL status via net_fetch","permission":"net"}]"#;
    alloc_str(json)
}

#[no_mangle]
pub extern "C" fn forge_tool_invoke(fn_ptr: i32, fn_len: i32, args_ptr: i32, args_len: i32) -> i64 {
    let fn_name = unsafe { read_str(fn_ptr, fn_len) };
    if fn_name != "urlcheck_status" {
        return error_envelope(&format!("unknown tool: {}", fn_name));
    }

    let args_bytes: Vec<u8> = unsafe {
        if args_len <= 0 {
            Vec::new()
        } else {
            std::slice::from_raw_parts(args_ptr as *const u8, args_len as usize).to_vec()
        }
    };

    let url = match extract_json_string(&args_bytes, b"\"url\"") {
        Some(v) if !v.is_empty() => v,
        _ => return error_envelope("missing url"),
    };

    // Call host net_fetch.
    let url_bytes = url.as_bytes();
    let packed = unsafe { net_fetch(url_bytes.as_ptr() as i32, url_bytes.len() as i32) };
    let (resp_ptr, resp_len) = unpack(packed);
    if resp_len == 0 {
        return error_envelope("net_fetch returned empty");
    }
    let resp_bytes: Vec<u8> = unsafe {
        std::slice::from_raw_parts(resp_ptr as *const u8, resp_len as usize).to_vec()
    };

    // If host returned an error envelope, forward it verbatim (re-allocate for ABI).
    if find_substring(&resp_bytes, b"\"error\"").is_some() {
        return alloc_bytes(&resp_bytes);
    }

    let status = parse_status(&resp_bytes).unwrap_or(0);
    let bytes_len = body_len_from_response(&resp_bytes);

    // Build success JSON: {"url": "...", "status": ..., "bytes": ...}
    // Use Debug for url to get JSON-quoted string.
    let out = format!(r#"{{"url":{:?},"status":{},"bytes":{}}}"#, url, status, bytes_len);
    alloc_str(&out)
}
