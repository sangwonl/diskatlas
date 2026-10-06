export const appVersion = __DISKATLAS_VERSION__;
export const updatePreview = __DISKATLAS_UPDATE_PREVIEW__;

const latestReleaseUrl = 'https://api.github.com/repos/sangwonl/diskatlas/releases/latest';
const releasePagePattern = /^https:\/\/github\.com\/sangwonl\/diskatlas\/releases\//;

function versionParts(version) {
  const match = String(version || '').replace(/^v/, '').match(/^(\d+)\.(\d+)\.(\d+)$/);
  return match ? match.slice(1).map(Number) : null;
}

function isNewerVersion(candidate, current) {
  const candidateParts = versionParts(candidate);
  const currentParts = versionParts(current);
  if (!candidateParts || !currentParts) return false;
  for (let index = 0; index < candidateParts.length; index += 1) {
    if (candidateParts[index] !== currentParts[index]) return candidateParts[index] > currentParts[index];
  }
  return false;
}

export async function checkForUpdate() {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 7000);
  try {
    const response = await fetch(latestReleaseUrl, {
      headers: { Accept: 'application/vnd.github+json' },
      cache: 'no-cache',
      signal: controller.signal,
    });
    if (response.status === 404) return null;
    if (!response.ok) throw new Error(`GitHub release check failed: ${response.status}`);

    const release = await response.json();
    const version = String(release.tag_name || '').replace(/^v/, '');
    if (!isNewerVersion(version, appVersion) || !releasePagePattern.test(release.html_url || '')) return null;

    return { version, url: release.html_url };
  } finally {
    window.clearTimeout(timeout);
  }
}
