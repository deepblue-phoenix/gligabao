# gligabao-server

The [gligabao](https://github.com/deepblue-phoenix/gligabao) server, as a prebuilt Go binary, installed with pip:

```sh
pip install gligabao-server
export ANTHROPIC_API_KEY=...
gligabao-server serve          # or: gligabao serve; listens on :7878
```

The Python client is a separate package, `pip install gligabao`: install the client where you call gligabao, and this
package where you run it. The wheels are platform specific (Linux, macOS and Windows, amd64 and arm64); each contains the
binary (with the admin UI embedded) and the default `gligabao.toml`, which the binary reads from its own directory unless
you pass `--config`. That copy is replaced when the package is upgraded, so keep your own settings in a file of yours and
run `gligabao-server serve --config /path/to/gligabao.toml`.

The Python sidecar (model training) is not part of this package.

```python
import gligabao_server

gligabao_server.binary_path()   # the binary
gligabao_server.config_path()   # the default gligabao.toml
```
