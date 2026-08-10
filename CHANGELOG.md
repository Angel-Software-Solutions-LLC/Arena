# Changelog

All notable changes to the Arena are recorded here. Format loosely follows Keep a Changelog.

## Unreleased

### Fixed
- Spectator arena rendered blank on every WebGPU-capable machine. Under Babylon's WebGPU backend the DefaultRenderingPipeline's bloom pass failed to bind its textures (`textureSampler`/`bloomBlur` missing in `getBindGroups`) and threw inside the render loop on the first frame, so no frame ever completed: measured 0 fps and 0 active meshes on the live page while the same `ArenaEngine` code on WebGL ran at 60 fps. The renderer now defaults to WebGL, which is also the only path the browser test suite has ever exercised (every spec deletes `navigator.gpu`), and keeps WebGPU reachable with `?webgpu=1`. (#228)
- Bumped the frontend cache-bust tags on both entry points (desktop and `m/`) so the renderer fix is actually refetched. The live edge serves `.js` with a four hour max-age, so a behaviour change behind an unchanged `?v=` URL would not reach anyone. (#228)
