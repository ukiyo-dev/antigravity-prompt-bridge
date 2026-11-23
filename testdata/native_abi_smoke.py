"""Exercise the exported C ABI against a local DLL or .so without a CPA server."""

import ctypes as c
import json
from pathlib import Path
import sys
import unittest


class Buffer(c.Structure):
    _fields_ = [("ptr", c.c_void_p), ("length", c.c_size_t)]


Call = c.CFUNCTYPE(c.c_int, c.c_char_p, c.c_void_p, c.c_size_t, c.POINTER(Buffer))
Free = c.CFUNCTYPE(None, c.c_void_p, c.c_size_t)
Shutdown = c.CFUNCTYPE(None)


class HostAPI(c.Structure):
    _fields_ = [
        ("version", c.c_uint32),
        ("context", c.c_void_p),
        ("call", c.c_void_p),
        ("free_buffer", c.c_void_p),
    ]


class PluginAPI(c.Structure):
    _fields_ = [
        ("version", c.c_uint32),
        ("call", Call),
        ("free_buffer", Free),
        ("shutdown", Shutdown),
    ]


class NativeABITest(unittest.TestCase):
    def test_init_and_calls(self):
        init = library.cliproxy_plugin_init
        init.argtypes = [c.POINTER(HostAPI), c.POINTER(PluginAPI)]
        init.restype = c.c_int

        api = PluginAPI()
        host = HostAPI(version=1)
        self.assertNotEqual(init(None, c.byref(api)), 0)
        self.assertNotEqual(init(c.byref(HostAPI(version=host.version + 1)), c.byref(api)), 0)
        self.assertNotEqual(init(c.byref(host), None), 0)
        self.assertFalse(api.call)
        self.assertEqual(init(c.byref(host), c.byref(api)), 0)
        self.assertEqual(api.version, host.version)
        self.assertTrue(api.call and api.free_buffer and api.shutdown)
        self.addCleanup(api.shutdown)

        register = b"plugin.register"
        self.assertNotEqual(api.call(register, None, 0, None), 0)
        dummy = c.c_uint8()
        oversized = 1 << (c.sizeof(c.c_int) * 8 - 1)
        for method, request, size, code in (
            (None, None, 0, "invalid_method"),
            (register, None, 1, "invalid_request"),
            (register, c.byref(dummy), oversized, "invalid_request"),
        ):
            with self.subTest(code=code, size=size):
                rc, envelope = self.call(api, method, request, size)
                self.assertNotEqual(rc, 0)
                self.assertFalse(envelope["ok"])
                self.assertEqual(envelope["error"]["code"], code)

        rc, envelope = self.call(api, register, None, 0)
        self.assertEqual(rc, 0)
        self.assertTrue(envelope["ok"])
        self.assertTrue(envelope["result"]["capabilities"]["request_normalizer"])
        metadata = envelope["result"]["metadata"]
        self.assertEqual(metadata["Name"], "Antigravity Prompt Bridge")
        fields = metadata["ConfigFields"]
        self.assertCountEqual(
            [field["Name"] for field in fields],
            ["mode", "ack_language", "custom_ack_text", "smart_min_bytes",
             "prefix_when_empty", "split_tool_groups"],
        )
        for field in fields:
            with self.subTest(field=field["Name"]):
                # CPA HTML-escapes descriptions before sending them to the panel.
                description = field["Description"]
                self.assertRegex(description, r"[\u4e00-\u9fff]")
                self.assertNotRegex(description, r"[&<>\x22\x27]")
        self.assertTrue(metadata["Author"] and metadata["GitHubRepository"])
        if expected_version is not None:
            self.assertEqual(metadata["Version"], expected_version)

    def call(self, api, method, request, size):
        response = Buffer()
        rc = api.call(method, request, size, c.byref(response))
        try:
            self.assertTrue(response.ptr and response.length)
            return rc, json.loads(c.string_at(response.ptr, response.length))
        finally:
            if response.ptr:
                api.free_buffer(response.ptr, response.length)


if __name__ == "__main__":
    if len(sys.argv) not in (2, 3):
        sys.exit(f"Usage: {sys.argv[0]} <library> [expected-version]")
    library = c.CDLL(str(Path(sys.argv[1]).resolve(strict=True)))
    expected_version = sys.argv[2] if len(sys.argv) == 3 else None
    del sys.argv[1:]
    unittest.main()
