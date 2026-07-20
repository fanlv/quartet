// QoderCN credential decrypt helper, invoked by the quartet usage service.
// Usage: node decrypt.mjs <authFile> <machineIdFile> <wasmB64File>
// Reads the WASM-encrypted credential blob, decrypts it with the embedded auth
// WASM (AES-GCM keyed by the machine id), and prints the userInfo JSON to stdout.
import { readFileSync } from 'node:fs';
import { webcrypto, randomFillSync } from 'node:crypto';

const [authFile, machineIdFile, wasmB64File] = process.argv.slice(2);
if (!authFile || !machineIdFile || !wasmB64File) {
  console.error('usage: node decrypt.mjs <authFile> <machineIdFile> <wasmB64File>');
  process.exit(2);
}

const wasmBytes = Buffer.from(readFileSync(wasmB64File, 'utf8').trim(), 'base64');
const encrypted = readFileSync(authFile, 'utf8').trim();
const key = readFileSync(machineIdFile, 'utf8').trim().slice(0, 16);

let wasm;
const heap = new Array(128).fill(undefined);
heap.push(undefined, null, true, false);
let heapNext = heap.length;

function addHeapObject(obj) {
  if (heapNext === heap.length) heap.push(heap.length + 1);
  const idx = heapNext;
  heapNext = heap[idx];
  heap[idx] = obj;
  return idx;
}
function getObject(idx) { return heap[idx]; }
function dropObject(idx) { if (idx < 132) return; heap[idx] = heapNext; heapNext = idx; }
function takeObject(idx) { const ret = getObject(idx); dropObject(idx); return ret; }

let WASM_VECTOR_LEN = 0;
let cachedUint8Memory = null;
let cachedInt32Memory = null;
const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });

function getUint8Memory() {
  if (cachedUint8Memory === null || cachedUint8Memory.byteLength === 0)
    cachedUint8Memory = new Uint8Array(wasm.memory.buffer);
  return cachedUint8Memory;
}
function getInt32Memory() {
  if (cachedInt32Memory === null || cachedInt32Memory.byteLength === 0)
    cachedInt32Memory = new Int32Array(wasm.memory.buffer);
  return cachedInt32Memory;
}

function passStringToWasm(arg, malloc, realloc) {
  let len = arg.length;
  let ptr = malloc(len, 1) >>> 0;
  const mem = getUint8Memory();
  let offset = 0;
  for (; offset < len; offset++) {
    const code = arg.charCodeAt(offset);
    if (code > 0x7f) break;
    mem[ptr + offset] = code;
  }
  if (offset !== len) {
    if (offset !== 0) arg = arg.slice(offset);
    ptr = realloc(ptr, len, (len = offset + arg.length * 3), 1) >>> 0;
    const view = getUint8Memory().subarray(ptr + offset, ptr + len);
    const ret = textEncoder.encodeInto(arg, view);
    offset += ret.written;
    ptr = realloc(ptr, len - offset, offset, 1) >>> 0;
    len = offset;
  }
  WASM_VECTOR_LEN = len;
  return ptr;
}

function getStringFromWasm(ptr, len) {
  return textDecoder.decode(getUint8Memory().subarray(ptr >>> 0, (ptr >>> 0) + len));
}

const imports = {
  './qoder_auth_wasm_bg.js': {
    __wbindgen_object_drop_ref: (idx) => { takeObject(idx); },
    __wbg_set_08463b1df38a7e29: (idx, keyIdx, valIdx) => { getObject(idx)[takeObject(keyIdx)] = takeObject(valIdx); },
    __wbg_getRandomValues_d49329ff89a07af1: (_idx, ptr, len) => {
      webcrypto.getRandomValues(getUint8Memory().subarray(ptr, ptr + len));
    },
    __wbg_crypto_38df2bab126b63dc: (idx) => addHeapObject(getObject(idx).crypto),
    __wbg_process_44c7a14e11e9f69e: (idx) => addHeapObject(getObject(idx).process),
    __wbg_versions_276b2795b1c6a219: (idx) => addHeapObject(getObject(idx).versions),
    __wbg_node_84ea875411254db1: (idx) => addHeapObject(getObject(idx).node),
    __wbg_require_b4edbdcf3e2a1ef0: () => addHeapObject(function fakeRequire() { return {}; }),
    __wbg_msCrypto_bd5a034af96bcba6: (idx) => addHeapObject(getObject(idx).msCrypto),
    __wbg_getRandomValues_c44a50d8cfdaebeb: (_idx, ptr, len) => {
      webcrypto.getRandomValues(getUint8Memory().subarray(ptr, ptr + len));
    },
    __wbg_randomFillSync_6c25eac9869eb53c: (_idx, ptr, len) => {
      randomFillSync(Buffer.from(getUint8Memory().buffer, ptr, len));
    },
    __wbg_call_d578befcc3145dee: (idx0, idx1, idx2) => {
      try { return addHeapObject(getObject(idx0).call(getObject(idx1), getObject(idx2))); }
      catch (e) { wasm.__wbindgen_export5(addHeapObject(e)); }
    },
    __wbindgen_object_clone_ref: (idx) => addHeapObject(getObject(idx)),
    __wbg_new_with_length_9cedd08484b73942: (len) => addHeapObject(new Uint8Array(len >>> 0)),
    __wbg_length_0c32cb8543c8e4c8: (idx) => getObject(idx).length,
    __wbg_prototypesetcall_3e05eb9545565046: (idx0, idx1, idx2) => {
      getObject(idx0).set(getObject(idx1), idx2 >>> 0);
      return 0;
    },
    __wbg_subarray_0f98d3fb634508ad: (idx, start, end) =>
      addHeapObject(getObject(idx).subarray(start >>> 0, end >>> 0)),
    __wbg_new_99cabae501c0a8a0: () => addHeapObject(new Uint8Array()),
    __wbg_now_88621c9c9a4f3ffc: () => Date.now(),
    __wbg_static_accessor_GLOBAL_THIS_a1248013d790bf5f: () => addHeapObject(globalThis),
    __wbg_static_accessor_SELF_24f78b6d23f286ea: () => addHeapObject(globalThis),
    __wbg_static_accessor_GLOBAL_f2e0f995a21329ff: () => addHeapObject(globalThis),
    __wbg_static_accessor_WINDOW_59fd959c540fe405: () => addHeapObject(globalThis),
    __wbg___wbindgen_throw_81fc77679af83bc6: (ptr, len) => { throw new Error(getStringFromWasm(ptr, len)); },
    __wbg_Error_2e59b1b37a9a34c3: (ptr, len) => addHeapObject(new Error(getStringFromWasm(ptr, len))),
    __wbg___wbindgen_is_object_40c5a80572e8f9d3: (idx) => {
      const v = getObject(idx);
      return typeof v === 'object' && v !== null ? 1 : 0;
    },
    __wbg___wbindgen_is_string_b29b5c5a8065ba1a: (idx) => (typeof getObject(idx) === 'string' ? 1 : 0),
    __wbg___wbindgen_is_function_49868bde5eb1e745: (idx) => (typeof getObject(idx) === 'function' ? 1 : 0),
    __wbg___wbindgen_is_undefined_c0cca72b82b86f4d: (idx) => (getObject(idx) === undefined ? 1 : 0),
    __wbindgen_cast_0000000000000001: (idx) => idx,
    __wbindgen_cast_0000000000000002: (idx) => idx,
  },
};

const result = await WebAssembly.instantiate(wasmBytes, imports);
wasm = result.instance.exports;

const retptr = wasm.__wbindgen_add_to_stack_pointer(-16);
const ptr0 = passStringToWasm(encrypted, wasm.__wbindgen_export2, wasm.__wbindgen_export3);
const len0 = WASM_VECTOR_LEN;
const ptr1 = passStringToWasm(key, wasm.__wbindgen_export2, wasm.__wbindgen_export3);
const len1 = WASM_VECTOR_LEN;

wasm.credential_storage_decrypt(retptr, ptr0, len0, ptr1, len1);

const mem = getInt32Memory();
const r0 = mem[retptr / 4 + 0];
const r1 = mem[retptr / 4 + 1];
const r2 = mem[retptr / 4 + 2];
const r3 = mem[retptr / 4 + 3];
if (r3) throw takeObject(r2);

process.stdout.write(getStringFromWasm(r0, r1));
