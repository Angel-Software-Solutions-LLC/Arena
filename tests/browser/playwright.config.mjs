import { defineConfig } from '@playwright/test';

const functionalWebGLArgs = [
  '--use-angle=swiftshader',
  '--enable-unsafe-swiftshader',
  '--disable-vulkan',
];
const fixturePort = process.env.ARENA_BROWSER_PORT || '42817';

export default defineConfig({
  testDir: './specs',
  outputDir: './test-results',
  // GitHub's single-core SwiftShader runner is intentionally much slower
  // than a hardware-backed browser. Keep a firm bound while allowing the
  // full multi-round lifecycle matrix to finish under software WebGL.
  timeout: 120_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: process.env.CI ? 1 : undefined,
  retries: process.env.CI ? 1 : 0,
  reporter: [
    ['line'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
  ],
  use: {
    baseURL: `http://127.0.0.1:${fixturePort}`,
    headless: true,
    trace: 'retain-on-failure',
    launchOptions: {
      args: functionalWebGLArgs,
      ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
        ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
        : {}),
    },
  },
  projects: [
    {
      name: 'phone-375',
      grepInvert: /@desktop-only/,
      use: { viewport: { width: 375, height: 812 } },
    },
    {
      name: 'tablet-768',
      grepInvert: /@(phone|desktop)-only/,
      use: { viewport: { width: 768, height: 900 } },
    },
    {
      name: 'desktop-1440',
      grepInvert: /@phone-only/,
      use: { viewport: { width: 1440, height: 900 } },
    },
  ],
  webServer: {
    command: 'node fixture-server.mjs',
    url: `http://127.0.0.1:${fixturePort}/__health`,
    env: { ARENA_BROWSER_PORT: fixturePort },
    reuseExistingServer: false,
    timeout: 20_000,
  },
});
