# Changelog

All notable changes to the Arena are recorded here. Format loosely follows Keep a Changelog.

## Unreleased

- Fixed the blank spectator arena. Babylon zeroes `_frameHandler` before running the render callback and only re-queues the next frame after it returns, with no try/catch on that path, so a single throwing frame ends rendering permanently. Measured on a live WebGPU client: `activeRenderLoops` 1, `_frameHandler` 0, `frameId` 0, canvas black, while the spectator socket kept streaming state and a hand-called `scene.render()` still drew 166 meshes. The render callback now contains its own throws: the first failure drops the bloom pass (whose bind-group failure was the observed trigger) and keeps drawing, repeated failures drop the post-process pipeline, and the scene itself never stops. WebGPU remains the default where supported because it is a large CPU saving; `?webgpu=0` forces WebGL for triage. Also bumped the cache-bust tags on both entry points, desktop and `m/`, since the live edge serves `.js` with a four hour max-age and an unchanged `?v=` URL would not be refetched. (#228)
### Fixed
