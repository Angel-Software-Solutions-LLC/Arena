# Changelog

All notable changes to the Arena are recorded here. Format loosely follows Keep a Changelog.

## Unreleased

### Fixed
- Spectator arena rendered blank on every WebGPU-capable machine. Babylon's WebGPU backend crashed the GlowLayer post-process (`textureSampler`/`bloomBlur` failed to bind in `getBindGroups`), throwing in the render loop on the first frame so nothing drew. The renderer now defaults to WebGL, which renders the identical scene including the glow, and keeps WebGPU reachable with `?webgpu=1` for anyone fixing the GlowLayer-on-WebGPU binding upstream. (#228)
