"""Hatchling build hook that gives the gligabao-server wheel its platform tag.

The wheel contains a prebuilt Go binary, so it is platform specific, not pure Python. ops/pypi.py builds one wheel per
platform and cross-compiles the binary, so the tag cannot be inferred from the machine that builds: it is passed in the
environment variable GLIGABAO_WHEEL_TAG as the platform part, e.g. "manylinux2014_x86_64" or "win_amd64", and the wheel
gets the tag "py3-none-<platform>".

This file lives in ops/ with the rest of the build code. ops/pypi.py copies it next to pyproject.toml in the staging
directory of each build (build/pypi/server-<os>-<arch>/), which is where hatchling finds it (pyproject.toml names it).
"""

import os

from hatchling.builders.hooks.plugin.interface import BuildHookInterface


class CustomBuildHook(BuildHookInterface):
    def initialize(self, version, build_data):
        platform = os.environ.get("GLIGABAO_WHEEL_TAG", "").strip()
        if not platform:
            raise RuntimeError("GLIGABAO_WHEEL_TAG is not set: build this package with ops/pypi.py")
        build_data["pure_python"] = False
        build_data["tag"] = f"py3-none-{platform}"
