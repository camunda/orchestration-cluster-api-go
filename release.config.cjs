const base = require('@camunda8/sdk-infra/configs/release.config.base.cjs');

// Go modules are consumed straight from the git tag (pkg.go.dev indexes it), so
// there is no registry publish step: a release is the vX.Y.Z tag plus a GitHub
// Release.
module.exports = {
  ...base,
  plugins: [
    ...base.plugins,
    [
      '@semantic-release/exec',
      {
        verifyReleaseCmd: 'bash scripts/verify-release-major.sh "${nextRelease.version}"',
      },
    ],
    [
      '@semantic-release/git',
      {
        assets: ['CHANGELOG.md'],
        message: 'chore(release): ${nextRelease.version} [skip ci]\n\n${nextRelease.notes}',
      },
    ],
    [
      '@semantic-release/github',
      {
        successComment: false,
      },
    ],
  ],
};
