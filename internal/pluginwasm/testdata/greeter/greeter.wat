(module
  (import "forge_host" "fs_read" (func $fs_read (param i32 i32) (result i64)))
  (import "forge_host" "log" (func $log (param i32 i32 i32 i32)))
  (import "forge_host" "fs_write" (func $fs_write (param i32 i32 i32 i32) (result i32)))
  (import "forge_host" "shell_exec" (func $shell_exec (param i32 i32 i32 i32) (result i64)))
  (import "forge_host" "git_run" (func $git_run (param i32 i32) (result i64)))
  (import "forge_host" "net_fetch" (func $net_fetch (param i32 i32) (result i64)))
  (memory (export "memory") 2)
  (global $heap (mut i32) (i32.const 2048))

  ;; static data
  (data (i32.const 100) "[{\"name\":\"greeter_greet\",\"description\":\"Greets a user and echoes a file via host fs_read\",\"permission\":\"fs.read\"}]")
  (data (i32.const 300) "hello ")
  (data (i32.const 310) " file:")
  (data (i32.const 320) "{\"greeting\":\"")
  (data (i32.const 340) "\"}")
  (data (i32.const 350) "{\"error\":\"unknown tool\"}")
  (data (i32.const 380) "greeter_greet")
  (data (i32.const 400) "\"name\"")
  (data (i32.const 410) "\"file\"")

  (func $alloc (export "forge_alloc") (param $size i32) (result i32)
    (local $ptr i32)
    (global.get $heap)
    (local.set $ptr)
    (global.set $heap (i32.add (global.get $heap) (local.get $size)))
    (local.get $ptr)
  )

  (func (export "forge_abi_version") (result i32)
    i32.const 1
  )

  (func (export "forge_tool_list") (result i64)
    (i64.or
      (i64.shl (i64.extend_i32_u (i32.const 100)) (i64.const 32))
      (i64.extend_i32_u (i32.const 114))
    )
  )

  ;; helper: memcmp ptr1 len1 ptr2 len2 -> 1 if equal else 0
  (func $memcmp (param $p1 i32) (param $l1 i32) (param $p2 i32) (param $l2 i32) (result i32)
    (local $i i32)
    (if (i32.ne (local.get $l1) (local.get $l2)) (then (i32.const 0) (return)))
    (local.set $i (i32.const 0))
    (loop $loop
      (if (i32.ge_u (local.get $i) (local.get $l1)) (then (i32.const 1) (return)))
      (if (i32.ne (i32.load8_u (i32.add (local.get $p1) (local.get $i))) (i32.load8_u (i32.add (local.get $p2) (local.get $i)))) (then (i32.const 0) (return)))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)
    )
    (i32.const 1)
  )

  ;; helper: find substring needle in haystack, return offset or -1
  ;; naive search
  (func $find (param $hay_ptr i32) (param $hay_len i32) (param $needle_ptr i32) (param $needle_len i32) (result i32)
    (local $i i32)
    (local $j i32)
    (local $match i32)
    (if (i32.gt_u (local.get $needle_len) (local.get $hay_len)) (then (i32.const -1) (return)))
    (local.set $i (i32.const 0))
    (loop $outer
      (if (i32.gt_u (i32.add (local.get $i) (local.get $needle_len)) (local.get $hay_len)) (then (i32.const -1) (return)))
      (local.set $match (i32.const 1))
      (local.set $j (i32.const 0))
      (block $break_inner
        (loop $inner
          (if (i32.ge_u (local.get $j) (local.get $needle_len)) (then (br $break_inner)))
          (if (i32.ne (i32.load8_u (i32.add (local.get $hay_ptr) (i32.add (local.get $i) (local.get $j)))) (i32.load8_u (i32.add (local.get $needle_ptr) (local.get $j)))) (then (local.set $match (i32.const 0)) (br $break_inner)))
          (local.set $j (i32.add (local.get $j) (i32.const 1)))
          (br $inner)
        )
      )
      (if (local.get $match) (then (local.get $i) (return)))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $outer)
    )
    (i32.const -1)
  )

  ;; helper: extract JSON string value for key: find "\"key\"" then ':' then '"' then capture until next '"'
  ;; returns (ptr len) packed? We'll return via out params using heap allocation.
  ;; For simplicity, we extract "name" and "file" values if present.
  ;; This function searches haystack for pattern and returns offset of value start and length.
  ;; Returns packed i64 of value ptr:len, or 0 if not found.
  (func $extract_value (param $hay_ptr i32) (param $hay_len i32) (param $key_ptr i32) (param $key_len i32) (result i64)
    (local $pos i32)
    (local $val_start i32)
    (local $val_end i32)
    (local $i i32)
    (local.set $pos (call $find (local.get $hay_ptr) (local.get $hay_len) (local.get $key_ptr) (local.get $key_len)))
    (if (i32.eq (local.get $pos) (i32.const -1)) (then (i64.const 0) (return)))
    (local.set $i (i32.add (local.get $pos) (local.get $key_len)))
    ;; find ':' after key
    (block $out_colon
      (loop $loop_colon
        (if (i32.ge_u (local.get $i) (local.get $hay_len)) (then (i64.const 0) (return)))
        (if (i32.eq (i32.load8_u (i32.add (local.get $hay_ptr) (local.get $i))) (i32.const 58))
          (then (local.set $i (i32.add (local.get $i) (i32.const 1))) (br $out_colon))
        )
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $loop_colon)
      )
    )
    ;; find opening '"'
    (block $out_q1
      (loop $loop_q1
        (if (i32.ge_u (local.get $i) (local.get $hay_len)) (then (i64.const 0) (return)))
        (if (i32.eq (i32.load8_u (i32.add (local.get $hay_ptr) (local.get $i))) (i32.const 34))
          (then (local.set $val_start (i32.add (local.get $i) (i32.const 1))) (local.set $i (i32.add (local.get $i) (i32.const 1))) (br $out_q1))
        )
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $loop_q1)
      )
    )
    ;; find closing '"'
    (local.set $val_end (local.get $val_start))
    (block $out_q2
      (loop $loop_q2
        (if (i32.ge_u (local.get $val_end) (local.get $hay_len)) (then (i64.const 0) (return)))
        (if (i32.eq (i32.load8_u (i32.add (local.get $hay_ptr) (local.get $val_end))) (i32.const 34))
          (then
            (i64.or
              (i64.shl (i64.extend_i32_u (local.get $val_start)) (i64.const 32))
              (i64.extend_i32_u (i32.sub (local.get $val_end) (local.get $val_start)))
            )
            (return)
          )
        )
        (local.set $val_end (i32.add (local.get $val_end) (i32.const 1)))
        (br $loop_q2)
      )
    )
    (i64.const 0)
  )

  (func (export "forge_tool_invoke") (param $fn_ptr i32) (param $fn_len i32) (param $args_ptr i32) (param $args_len i32) (result i64)
    (local $is_greeter i32)
    (local $name_ptr i32) (local $name_len i32)
    (local $file_ptr i32) (local $file_len i32)
    (local $packed i64)
    (local $file_content_ptr i32) (local $file_content_len i32)
    (local $greeting_ptr i32) (local $greeting_len i32)
    (local $out_ptr i32) (local $out_len i32)
    (local $decoded_ptr i32) (local $decoded_len i32)
    (local $tmp i32)
    (local $total_len i32)

    ;; check fn name == "greeter_greet"
    (local.set $is_greeter (call $memcmp (local.get $fn_ptr) (local.get $fn_len) (i32.const 380) (i32.const 13)))
    (if (i32.eqz (local.get $is_greeter))
      (then
        (i64.or (i64.shl (i64.extend_i32_u (i32.const 350)) (i64.const 32)) (i64.extend_i32_u (i32.const 26)))
        (return)
      )
    )

    ;; extract "name" value
    (local.set $packed (call $extract_value (local.get $args_ptr) (local.get $args_len) (i32.const 400) (i32.const 6)))
    (if (i64.ne (local.get $packed) (i64.const 0))
      (then
        (local.set $name_ptr (i32.wrap_i64 (i64.shr_u (local.get $packed) (i64.const 32))))
        (local.set $name_len (i32.wrap_i64 (local.get $packed)))
        ;; convert ptr from offset within args to absolute: name_ptr is offset within args buffer's address space? Actually extract_value returns offset within haystack's memory region? We returned val_start as absolute offset within haystack's base? Wait val_start is offset from hay_ptr, not absolute. Need to add hay_ptr.
        ;; Our extract currently returned val_start as index from start of haystack region (relative), not absolute. Fix: add hay_ptr.
        (local.set $name_ptr (i32.add (local.get $args_ptr) (local.get $name_ptr)))
      )
      (else
        (local.set $name_ptr (i32.const 0))
        (local.set $name_len (i32.const 0))
      )
    )
    ;; default handled later via allocation if len==0

    ;; extract "file" value
    (local.set $packed (call $extract_value (local.get $args_ptr) (local.get $args_len) (i32.const 410) (i32.const 6)))
    (if (i64.ne (local.get $packed) (i64.const 0))
      (then
        (local.set $file_ptr (i32.wrap_i64 (i64.shr_u (local.get $packed) (i64.const 32))))
        (local.set $file_len (i32.wrap_i64 (local.get $packed)))
        (local.set $file_ptr (i32.add (local.get $args_ptr) (local.get $file_ptr)))
      )
      (else
        (local.set $file_ptr (i32.const 0))
        (local.set $file_len (i32.const 0))
      )
    )

    ;; if file present, call host fs_read
    (local.set $file_content_ptr (i32.const 0))
    (local.set $file_content_len (i32.const 0))
    (if (i32.gt_u (local.get $file_len) (i32.const 0))
      (then
        (local.set $packed (call $fs_read (local.get $file_ptr) (local.get $file_len)))
        ;; unpack host result: ptr in high, len low
        (local.set $tmp (i32.wrap_i64 (i64.shr_u (local.get $packed) (i64.const 32))))
        (local.set $file_content_len (i32.wrap_i64 (local.get $packed)))
        (local.set $file_content_ptr (local.get $tmp))
        ;; host returns JSON quoted string like "\"content\"" or error envelope
        ;; If it is JSON quoted, strip quotes: if first char == '"' and last == '"', strip
        (if (i32.gt_u (local.get $file_content_len) (i32.const 1))
          (then
            (if (i32.and (i32.eq (i32.load8_u (local.get $file_content_ptr)) (i32.const 34)) (i32.eq (i32.load8_u (i32.add (local.get $file_content_ptr) (i32.sub (local.get $file_content_len) (i32.const 1)))) (i32.const 34)))
              (then
                (local.set $file_content_ptr (i32.add (local.get $file_content_ptr) (i32.const 1)))
                (local.set $file_content_len (i32.sub (local.get $file_content_len) (i32.const 2)))
              )
            )
          )
        )
        ;; handle error envelope: if content starts with '{' and contains "error", keep as is? We'll check first char '{' then search for "error" substring; if found, treat file content as error prefix.
        ;; Simplify: if first char == '{', keep raw (include braces), else stripped.
      )
    )

    ;; Build greeting: "hello " + name (or "world") + " file:" + file_content (if any)
    ;; For simplicity, if name_len==0 use "world"
    (if (i32.eq (local.get $name_len) (i32.const 0))
      (then
        ;; allocate "world"
        (local.set $name_ptr (call $alloc (i32.const 5)))
        (i32.store8 (local.get $name_ptr) (i32.const 119)) ;; w
        (i32.store8 (i32.add (local.get $name_ptr) (i32.const 1)) (i32.const 111)) ;; o
        (i32.store8 (i32.add (local.get $name_ptr) (i32.const 2)) (i32.const 114)) ;; r
        (i32.store8 (i32.add (local.get $name_ptr) (i32.const 3)) (i32.const 108)) ;; l
        (i32.store8 (i32.add (local.get $name_ptr) (i32.const 4)) (i32.const 100)) ;; d
        (local.set $name_len (i32.const 5))
      )
    )

    ;; compute greeting length: 6 + name_len + (file? 6+file_len :0)
    (local.set $greeting_len (i32.add (i32.const 6) (local.get $name_len)))
    (if (i32.gt_u (local.get $file_content_len) (i32.const 0))
      (then (local.set $greeting_len (i32.add (local.get $greeting_len) (i32.add (i32.const 6) (local.get $file_content_len)))))
    )
    (local.set $greeting_ptr (call $alloc (local.get $greeting_len)))
    ;; copy "hello " (6)
    (memory.copy (local.get $greeting_ptr) (i32.const 300) (i32.const 6))
    ;; copy name
    (memory.copy (i32.add (local.get $greeting_ptr) (i32.const 6)) (local.get $name_ptr) (local.get $name_len))
    (if (i32.gt_u (local.get $file_content_len) (i32.const 0))
      (then
        ;; copy " file:"
        (memory.copy (i32.add (local.get $greeting_ptr) (i32.add (i32.const 6) (local.get $name_len))) (i32.const 310) (i32.const 6))
        ;; copy file content
        (memory.copy (i32.add (local.get $greeting_ptr) (i32.add (i32.add (i32.const 6) (local.get $name_len)) (i32.const 6))) (local.get $file_content_ptr) (local.get $file_content_len))
      )
    )

    ;; Build JSON object: {"greeting":"<greeting>"}
    ;; total = 13 + greeting_len + 2
    (local.set $total_len (i32.add (i32.add (i32.const 13) (local.get $greeting_len)) (i32.const 2)))
    (local.set $out_ptr (call $alloc (local.get $total_len)))
    (memory.copy (local.get $out_ptr) (i32.const 320) (i32.const 13))
    (memory.copy (i32.add (local.get $out_ptr) (i32.const 13)) (local.get $greeting_ptr) (local.get $greeting_len))
    (memory.copy (i32.add (local.get $out_ptr) (i32.add (i32.const 13) (local.get $greeting_len))) (i32.const 340) (i32.const 2))

    (i64.or (i64.shl (i64.extend_i32_u (local.get $out_ptr)) (i64.const 32)) (i64.extend_i32_u (local.get $total_len)))
  )
)
