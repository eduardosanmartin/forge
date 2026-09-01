(module
  (memory (export "memory") 1)
  (global $heap (mut i32) (i32.const 1024))
  (func (export "forge_alloc") (param i32) (result i32) (global.get $heap))
  (func (export "forge_abi_version") (result i32) i32.const 999)
  (func (export "forge_tool_list") (result i64) i64.const 0)
  (func (export "forge_tool_invoke") (param i32 i32 i32 i32) (result i64) i64.const 0)
)
