# Changelog

All notable changes to the Arena are recorded here. Format loosely follows Keep a Changelog.

## Unreleased

- Fixed the round transition never releasing, which left the arena black with only the emissive glow drawn while the HUD, kill feed and minimap kept updating. The enter path reads the round as `round_number ?? round`, but the release path read only `round_number`, so a payload carrying `round` entered the transition and could never leave it (`Number(undefined)` is NaN, and the release predicate requires both values finite). The map teardown that starts the transition then became permanent, which is the reported "it was visible, then went blank when the round started". Both paths now read the round the same way, with a test pinning the symmetry. (#228)
- Fixed the blank spectator arena. Babylon zeroes `_frameHandler` before running the render callback and only re-queues the next frame after it returns, with no try/catch on that path, so a single throwing frame ends rendering permanently. Measured on a live WebGPU client: `activeRenderLoops` 1, `_frameHandler` 0, `frameId` 0, canvas black, while the spectator socket kept streaming state and a hand-called `scene.render()` still drew 166 meshes. The render callback now contains its own throws: the first failure drops the bloom pass (whose bind-group failure was the observed trigger) and keeps drawing, repeated failures drop the post-process pipeline, and the scene itself never stops. WebGPU remains the default where supported because it is a large CPU saving; `?webgpu=0` forces WebGL for triage. Also bumped the cache-bust tags on both entry points, desktop and `m/`, since the live edge serves `.js` with a four hour max-age and an unchanged `?v=` URL would not be refetched. (#228)
### Fixed
