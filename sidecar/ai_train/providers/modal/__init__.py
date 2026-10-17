"""Everything Modal specific: the `Provider` implementation (provider.py) and the admin scripts the sidecar runs
(deploy, stop, list_apps, list_volumes, inference_containers). The only package that imports `modal`.

The scripts are run from the ai_train directory as modules, e.g. `python -m providers.modal.deploy`, so that `app`,
`config` and `providers` import as they do for app.py.
"""
