# IntelliJ IDEA Configuration

IntelliJ IDEA run configurations are configured for stellarCD development and testing.

## Run Configurations

All configurations are available in the **Run** menu or via Ctrl+Alt+R (Run dropdown).

### Build & Development

- **make: build** — Compile the manager binary
- **make: fmt** — Format Go code
- **make: vet** — Run Go vet
- **make: lint** — Run golangci-lint
- **make: lint-fix** — Fix linting issues
- **make: test** — Run all unit tests

### Minikube Workflow

- **minikube: check** — Verify Docker, kubectl, and minikube setup
- **minikube: build** — Build Docker images locally
- **minikube: load** — Load images into minikube's Docker daemon
- **minikube: deploy** — Deploy to minikube cluster
- **minikube: full workflow** — Execute all steps (build → load → deploy)

## Quick Setup

### 1. First Time

1. Open **Run → Edit Configurations** or press `Ctrl+Alt+R`
2. Configurations should auto-load from `.idea/runConfigurations/`
3. If not visible, reload the IDE: `File → Invalidate Caches → Invalidate and Restart`

### 2. Customize Environment Variables

Edit any configuration:
1. Select from run dropdown (top-right)
2. Click **Edit** or press `Ctrl+Alt+R` → pencil icon
3. Modify **Environment variables** (e.g., change `CONTROLLER_IMG`)

### 3. Execute

- **Click run icon** (▶️) in top toolbar
- **Or press Shift+F10** (default hotkey)
- Output appears in **Run** tool window (bottom)

## Environment Variables

All Minikube configurations accept these env vars:

```bash
CONTROLLER_IMG=controller:latest    # Controller image tag
UI_IMG=ui:latest                    # UI image tag
MINIKUBE_PROFILE=minikube           # Minikube cluster profile name
```

Edit in configuration dialog → **Environment variables** field.

## Workflow Examples

### Local Build & Test

```
1. make: build
2. make: fmt
3. make: test
```

### Deploy to Minikube

```
1. minikube: check           (verify environment)
2. minikube: full workflow   (build + load + deploy)
```

Or step-by-step:

```
1. minikube: build
2. minikube: load
3. minikube: deploy
```

### Code Quality Check

```
make: fmt
make: vet
make: lint
```

Fix issues:

```
make: lint-fix
```

## Troubleshooting

### Configurations not appearing

1. **Reload IDE:** `File → Invalidate Caches → Invalidate and Restart`
2. **Check .idea/runConfigurations/** — must contain `.xml` files

### Environment variables not set

1. Edit configuration: `Ctrl+Alt+R` → pencil icon
2. Verify **Environment variables** section
3. Reload IDE if needed

### Script path not found

Run configurations use **relative paths from project root**:
- `scripts/minikube-build.sh` is correct
- `/full/path/scripts/minikube-build.sh` is incorrect

### Terminal not interactive

Some configurations run in terminal mode. Output appears in the **Run** tool window:
- Scroll up/down to view full output
- Press **Stop** to interrupt

## Makefile Targets Reference

View all available targets:

```
make help
```

Key targets:
- `build` — Compile binary
- `test` — Run tests
- `docker-build` — Build images for registry
- `deploy` — Deploy to active kubectl cluster
- `minikube-*` — Minikube workflows (see Minikube section above)

## Adding New Configurations

To add a new run configuration:

1. Create `.idea/runConfigurations/my_config.xml`
2. Use existing files as template
3. Reload IDE or restart

Example template:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<component name="ProjectRunConfigurationManager">
  <configuration default="false" name="my task" type="ShConfigurationType">
    <option name="SCRIPT_TEXT" value="make my-target" />
    <option name="INDEPENDENT_SCRIPT_PATH" value="false" />
    <option name="SCRIPT_PATH" value="" />
    <option name="SCRIPT_OPTIONS" value="" />
    <option name="INDEPENDENT_SCRIPT_OPTIONS" value="true" />
    <option name="EXECUTE_IN_TERMINAL" value="true" />
    <option name="EXECUTE_SCRIPT_FILE" value="false" />
    <envs />
    <method v="2" />
  </configuration>
</component>
```

## Notes

- All scripts are in `.idea/runConfigurations/` and are checked into git
- Configurations are IDE-wide, not user-specific
- Terminal mode allows interactive output and Ctrl+C to stop
- Exit codes appear in the Run tool window status bar
