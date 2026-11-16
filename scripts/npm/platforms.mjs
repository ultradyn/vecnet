// BUILD_INFO.json uses Node platform/architecture names; Go needs GOOS/GOARCH.
// Keep the build, license inventory and artifact tests in sync.
export const nativeTargets = [
  { platform: 'linux-x64', goos: 'linux', goarch: 'amd64', suffix: '' },
  { platform: 'linux-arm64', goos: 'linux', goarch: 'arm64', suffix: '' },
  { platform: 'win32-x64', goos: 'windows', goarch: 'amd64', suffix: '.exe' },
  { platform: 'win32-arm64', goos: 'windows', goarch: 'arm64', suffix: '.exe' },
];
