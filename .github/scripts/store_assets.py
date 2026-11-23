"""Build and validate CLIProxyAPI store assets (stdlib only)."""

import argparse
import hashlib
from pathlib import Path
import re
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo

PLUGIN = "antigravity-prompt-bridge"
EXT = {"windows": "dll", "linux": "so", "darwin": "dylib"}
ARCHES = ("amd64", "arm64")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def library_name(os):
    return f"{PLUGIN}.{EXT[os]}"


def archive_name(version, os, arch):
    return f"{PLUGIN}_{version}_{os}_{arch}.zip"


def verify_library(data, os, arch):
    if os == "windows":
        offset = int.from_bytes(data[60:64], "little")
        signature = data[:2] == b"MZ" and data[offset:offset + 4] == b"PE\0\0"
        machine = int.from_bytes(data[offset + 4:offset + 6], "little")
        expected = {"amd64": 0x8664, "arm64": 0xAA64}[arch]
    elif os == "linux":
        signature = data.startswith(b"\x7fELF\x02\x01")
        machine = int.from_bytes(data[18:20], "little")
        expected = {"amd64": 62, "arm64": 183}[arch]
    else:
        signature = data.startswith(b"\xcf\xfa\xed\xfe")
        machine = int.from_bytes(data[4:8], "little")
        expected = {"amd64": 0x01000007, "arm64": 0x0100000C}[arch]
    require(signature and machine == expected, f"unexpected binary format for {os}/{arch}")
    markers = ["users\\runner", "users/runner", "home/runner/work", "gid___"]
    lower = data.lower()
    for marker in markers:
        require(not any(marker.encode(enc) in lower for enc in ("ascii", "utf-16le")), "private marker in library")
    require(not re.search(rb"(?:gh[pousr]_|github_pat_|AIza|sk-proj-)[A-Za-z0-9_=-]{20,}", data), "credential-like bytes in library")


def package(version, os, arch, directory):
    name = library_name(os)
    data = (directory / name).read_bytes()
    verify_library(data, os, arch)
    path = directory / archive_name(version, os, arch)
    with ZipFile(path, "w") as zip_file:
        entry = ZipInfo(name)
        entry.create_system = 3
        entry.external_attr = 0o100755 << 16
        zip_file.writestr(entry, data, compress_type=ZIP_DEFLATED)
    print(path)


def verify(version, directory):
    expected = {archive_name(version, os, arch): (os, arch) for os in EXT for arch in ARCHES}
    require({p.name for p in directory.glob("*.zip")} == set(expected), "expected exactly six platform archives")
    lines = []
    for name, (os, arch) in sorted(expected.items()):
        path = directory / name
        with ZipFile(path) as zip_file:
            require(zip_file.namelist() == [library_name(os)], f"unexpected ZIP layout: {name}")
            verify_library(zip_file.read(library_name(os)), os, arch)
        lines.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {name}\n")
    (directory / "checksums.txt").write_bytes("".join(lines).encode("ascii"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("directory", type=Path)
    parser.add_argument("--os", choices=EXT)
    parser.add_argument("--arch", choices=ARCHES)
    args = parser.parse_args()
    require(re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", args.version), "version must be numeric x.y.z")
    require(bool(args.os) == bool(args.arch), "specify both --os and --arch or neither")
    if args.os:
        package(args.version, args.os, args.arch, args.directory)
    else:
        verify(args.version, args.directory)


if __name__ == "__main__":
    main()
