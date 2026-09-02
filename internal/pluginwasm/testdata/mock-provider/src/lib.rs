// Mock LLM provider plugin — ABI v2, deterministic streaming.
// Streams a fixed reply in 4 content chunks then finish. If the request JSON
// contains the marker "mock_tool", streams a tool_call variant instead.
// ABI: forge_abi_version() -> 2, forge_llm_stream_start, forge_llm_next_chunk, forge_llm_cancel
// Memory: bump allocator, packed ptr/len JSON over linear memory (v1 conventions).

#![allow(static_mut_refs)]

static mut HEAP: [u8; 4194304] = [0; 4194304];
static mut HEAP_POS: usize = 0;

#[no_mangle]
pub extern "C" fn forge_abi_version() -> i32 {
    2
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

unsafe fn read_bytes(ptr: i32, len: i32) -> Vec<u8> {
    if len <= 0 {
        return Vec::new();
    }
    std::slice::from_raw_parts(ptr as *const u8, len as usize).to_vec()
}

fn find_substring(hay: &[u8], needle: &[u8]) -> bool {
    if needle.len() > hay.len() {
        return false;
    }
    for i in 0..=hay.len() - needle.len() {
        if &hay[i..i + needle.len()] == needle {
            return true;
        }
    }
    false
}

// --- Per-request state: single concurrent request (req_id=1) ---
struct ReqState {
    chunks: usize, // total number of content chunks to emit
    next: usize,   // next chunk index to emit
    use_tool: bool,
    done: bool,
}

static mut REQ: Option<ReqState> = None;
static mut NEXT_REQ_ID: u32 = 1;

#[no_mangle]
pub extern "C" fn forge_llm_stream_start(req_ptr: i32, req_len: i32) -> i64 {
    let req_bytes = unsafe { read_bytes(req_ptr, req_len) };
    // Detect marker for tool variant.
    let use_tool = find_substring(&req_bytes, b"mock_tool");
    unsafe {
        REQ = Some(ReqState {
            chunks: 4,
            next: 0,
            use_tool,
            done: false,
        });
        let id = NEXT_REQ_ID;
        // Keep NEXT_REQ_ID at 1 for determinism (single slot); but if we wanted increment, do it.
        // We keep it at 1 so cancel/next use stable id; but start always returns 1.
        let _ = id;
        let json = format!(r#"{{"req_id":{}}}"#, 1);
        return alloc_str(&json);
    }
}

fn stream_chunk_json(content: &str, index: usize, finish: Option<&str>) -> String {
    let finish_json = match finish {
        Some(v) => format!(r#""{}""#, v),
        None => "null".to_string(),
    };
    // Include model/id for completeness; not required but useful.
    format!(
        r#"{{"id":"mock-chunk-{}","model":"mock_provider","choices":[{{"index":0,"delta":{{"role":"assistant","content":{:?}}},"finish_reason":{}}}],"usage":null}}"#,
        index, content, finish_json
    )
}

fn tool_chunk_json(index: usize) -> String {
    format!(
        r#"{{"id":"mock-chunk-{}","model":"mock_provider","choices":[{{"index":0,"delta":{{"role":"assistant","content":"","tool_calls":[{{"id":"call_1","type":"function","function":{{"name":"mock_provider_echo","arguments":"{{\\\"msg\\\":\\\"hi\\\"}}"}}}}]}},"finish_reason":"tool_calls"}}],"usage":null}}"#,
        index
    )
}

#[no_mangle]
pub extern "C" fn forge_llm_next_chunk(req_id: i32) -> i64 {
    unsafe {
        if req_id != 1 {
            let err = r#"{"error":"unknown req_id","code":1}"#;
            return alloc_str(err);
        }
        let state = match REQ.as_mut() {
            Some(s) => s,
            None => {
                let err = r#"{"error":"no active request","code":1}"#;
                return alloc_str(err);
            }
        };
        if state.done {
            return alloc_str(r#"{"done":true}"#);
        }
        // Content pieces.
        let pieces = ["Hello ", "from ", "mock ", "provider!"];
        if state.use_tool {
            // For tool variant: first 2 chunks are text, last chunk is tool call.
            if state.next < 2 {
                let c = pieces[state.next];
                let json = stream_chunk_json(c, state.next, None);
                state.next += 1;
                return alloc_str(&json);
            } else if state.next == 2 {
                // Tool call chunk.
                let json = tool_chunk_json(state.next);
                state.next += 1;
                return alloc_str(&json);
            } else {
                state.done = true;
                return alloc_str(r#"{"done":true}"#);
            }
        } else {
            if state.next < state.chunks {
                let is_last = state.next == state.chunks - 1;
                let c = pieces[state.next];
                let finish = if is_last { Some("stop") } else { None };
                let json = stream_chunk_json(c, state.next, finish);
                state.next += 1;
                if is_last {
                    // Next call will return done, but keep done=false until next pull.
                    // To make host see done after consuming last chunk, set done after this.
                    state.done = true;
                }
                // For last chunk, still return the chunk; next call will be done.
                // If we already set done, we need to allow one more done return.
                // So we return chunk now; on next invocation we return done.
                // To avoid immediate done, we flip back to not done for one extra turn?
                // Instead, keep done=false for this return and set a pending flag.
                // Simplest: if is_last, set a flag to return done next time, not now.
                // We set state.done = false initially and will set true after emitting last?
                // Actually we set done=true but we still returned chunk; next call will see done==true and return done.
                // However we just returned chunk, so we need done to be pending for next call, not this.
                // Revert: we shouldn't treat done==true as immediate done for this chunk.
                // So set done pending only for next invocation. We already set done=true, but we returned chunk instead of done, so next call will correctly return done.
                // That's fine — we already returned chunk, not done. The done flag is for next call.
                // But we already returned chunk; the done flag being true now means next call returns done.
                // For non-last, done stays false.
                // For tool variant, we handled separately.
                if is_last {
                    // Undo immediate done check for future: keep next==chunks and done=false until next call?
                    // Actually we did set done=true, but we returned chunk, so next call will see done=true and return done.
                    // But we want next call to return done, so keep done=true.
                }
                return alloc_str(&json);
            } else {
                // Should not reach when chunks already emitted, but handle.
                state.done = true;
                return alloc_str(r#"{"done":true}"#);
            }
        }
    }
}

#[no_mangle]
pub extern "C" fn forge_llm_cancel(req_id: i32) -> i32 {
    unsafe {
        if req_id == 1 {
            REQ = None;
            return 0;
        }
        1
    }
}

// Provider has no tools, but satisfy optional export for host that may probe.
#[no_mangle]
pub extern "C" fn forge_tool_list() -> i64 {
    alloc_str("[]")
}

#[no_mangle]
pub extern "C" fn forge_tool_invoke(_fn_ptr: i32, _fn_len: i32, _args_ptr: i32, _args_len: i32) -> i64 {
    alloc_str(r#"{"error":"provider has no tools"}"#)
}
