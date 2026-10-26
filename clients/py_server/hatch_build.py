"""Marks the wheel as platform specific (it contains a Go binary) and sets its tag.

run_pypi.sh builds one wheel per platform, cross-compiling the binary, so the tag cannot be inferred from the machine
that builds: it is passed in GLIGABAO_WHEEL_TAG as the platform part, e.g. "manylinux2014_x86_64" or "win_amd64".
"""

import os

from hatchling.builders.hooks.plugin.interface import BuildHookInterface


class CustomBuildHook(BuildHookInterface):
    def initialize(self, version, build_data):
        platform = os.environ.get("GLIGABAO_WHEEL_TAG", "").strip()
        if not platform:
            raise RuntimeError("GLIGABAO_WHEEL_TAG is not set: build this package with run_pypi.sh")
        build_data["pure_python"] = False
        build_data["tag"] = f"py3-none-{platform}"
