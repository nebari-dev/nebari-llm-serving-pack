import { describe, it, expect } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const docs = (name) =>
  fileURLToPath(new URL(`../src/content/docs/${name}`, import.meta.url));
const example = (name) =>
  fileURLToPath(new URL(`../../examples/${name}`, import.meta.url));

export const readPage = (name) => readFileSync(docs(name), 'utf8');
export const readExample = (name) => readFileSync(example(name), 'utf8');

describe('architecture: PassthroughModel embed', () => {
  it('is an .mdx page that embeds the real example file', () => {
    expect(existsSync(docs('architecture.mdx'))).toBe(true);
    const page = readPage('architecture.mdx');
    expect(page).toMatch(
      /import\s+\w+\s+from\s+'\.\.\/\.\.\/\.\.\/\.\.\/examples\/passthrough-openrouter\.yaml\?raw'/,
    );
    expect(page).toMatch(/<Code\s+code=\{/);
  });

  it('does not keep an inline duplicate of the manifest', () => {
    const page = readPage('architecture.mdx');
    // No fenced yaml block declaring the kind should remain on the page.
    expect(page).not.toMatch(/```yaml[\s\S]*kind:\s*PassthroughModel/);
  });
});

describe('new example manifests', () => {
  it('minimal.yaml uses the operator namespace the webhook requires', () => {
    const m = readExample('models/minimal.yaml');
    expect(m).toMatch(/namespace:\s*nebari-llm-serving-system/);
    expect(m).toMatch(/kind:\s*LLMModel/);
    expect(m).not.toMatch(/namespace:\s*llm-serving\b/);
  });

  it('advanced-scheduling.yaml demonstrates all spec.advanced escape hatches', () => {
    const m = readExample('models/advanced-scheduling.yaml');
    for (const field of ['nodeSelector', 'tolerations', 'affinity', 'extraArgs', 'extraEnv']) {
      expect(m).toContain(field);
    }
  });
});

describe('quickstart', () => {
  it('has a Route to an external provider section that embeds the example', () => {
    const page = readPage('quickstart.mdx');
    expect(page).toMatch(/##\s+Route to an external provider/);
    expect(page).toMatch(
      /import\s+\w+\s+from\s+'\.\.\/\.\.\/\.\.\/\.\.\/examples\/passthrough-openrouter\.yaml\?raw'/,
    );
    expect(page).toMatch(/kubectl create secret generic openrouter-api-key/);
    expect(page).toMatch(/llm\.[^\s]*\/v1\/chat\/completions/);
  });

  it('shows how to create the hf-token secret inline where authSecretName appears', () => {
    const page = readPage('quickstart.mdx');
    expect(page).toMatch(/kubectl create secret generic hf-token/);
    expect(page).toMatch(/HF_TOKEN/);
  });
});

describe('configuration', () => {
  it('documents every PassthroughModel field', () => {
    const page = readPage('configuration.mdx');
    expect(page).toMatch(/##\s+PassthroughModel CRD reference/);
    for (const f of [
      'provider.hostname', 'provider.port', 'provider.schemaVersion',
      'provider.credentialSecretName', 'models.catchAll', 'models.declared',
      'access', 'endpoints',
    ]) {
      expect(page).toContain(f);
    }
  });

  it('embeds minimal and advanced examples instead of inlining them', () => {
    const page = readPage('configuration.mdx');
    expect(page).toMatch(/examples\/models\/minimal\.yaml\?raw/);
    expect(page).toMatch(/examples\/models\/advanced-scheduling\.yaml\?raw/);
    // the broken hand-written minimal manifest is gone
    expect(page).not.toMatch(/namespace:\s*llm-serving\b/);
  });
});

describe('shared-storage', () => {
  it('embeds complete OCI and gated-HF manifests', () => {
    const page = readPage('shared-storage.mdx');
    expect(page).toMatch(/examples\/models\/oci-model\.yaml\?raw/);
    expect(page).toMatch(/examples\/models\/devstral-small\.yaml\?raw/);
  });

  it('devstral example header comment uses the operator namespace', () => {
    const m = readExample('models/devstral-small.yaml');
    expect(m).not.toMatch(/create secret generic hf-token -n llm-serving\b/);
  });
});

describe('troubleshooting', () => {
  it('has an external-provider (PassthroughModel) failure section', () => {
    const page = readPage('troubleshooting.md');
    expect(page).toMatch(/PassthroughModel|external provider/i);
    expect(page).toMatch(/ApplyFailed/);
    expect(page).toMatch(/credentialSecretName/);
    expect(page).toMatch(/catch-all|catchAll/i);
  });
});

describe('manifest reconciliation (NIC source of truth)', () => {
  it('argocd-application.yaml uses the published OCI chart (0.1.2) + NIC clusterIssuer + longhorn', () => {
    const m = readExample('argocd-application.yaml');
    expect(m).toMatch(/repoURL:\s*quay\.io\/nebari\/charts\b/);
    expect(m).toMatch(/chart:\s*nebari-llm-serving\b/);
    expect(m).toMatch(/targetRevision:\s*"0\.1\.2"/);
    expect(m).toMatch(/clusterIssuer:\s*letsencrypt-issuer\b/);
    expect(m).toMatch(/storageClassName:\s*longhorn\b/);
    expect(m).not.toMatch(/path:\s*charts\/nebari-llm-serving\b/);
    expect(m).not.toMatch(/v0\.1\.0-alpha\.7\b/);
    expect(m).not.toMatch(/letsencrypt-production\b/);
  });
  it('nvidia-gpu-operator.yaml uses the documented v25.10.1', () => {
    const m = readExample('nvidia-gpu-operator.yaml');
    expect(m).toMatch(/targetRevision:\s*v25\.10\.1\b/);
    expect(m).not.toMatch(/v26\.3\.0\b/);
  });
  it('ai-gateway-prereqs.yaml drops the oci:// prefix (NIC form)', () => {
    const m = readExample('ai-gateway-prereqs.yaml');
    expect(m).not.toMatch(/oci:\/\//);
    expect(m).toMatch(/repoURL:\s*docker\.io\/envoyproxy\b/);
  });
  it('ai-gateway-prereqs.yaml carries all three prerequisites with nothing to fill in', () => {
    const m = readExample('ai-gateway-prereqs.yaml');
    // One file, three Applications: the AI Gateway CRDs, the GIE CRDs, the controller.
    expect(m).toMatch(/name:\s*envoy-ai-gateway-crds\b/);
    expect(m).toMatch(/name:\s*gateway-api-inference-extension\b/);
    expect(m).toMatch(/name:\s*envoy-ai-gateway\b/);
    // Packs go in nebari-apps; foundational refuses their sources (NIC #481).
    expect(m).not.toMatch(/project:\s*foundational\b/);
    expect(m).toMatch(/project:\s*nebari-apps\b/);
    // The point of the file: applied as-is, so no placeholder may survive.
    expect(m).not.toMatch(/<[a-z0-9_-]+>/);
    // The GIE CRDs come from upstream's own kustomize dir, not a hand-authored
    // one in the user's repo - that is what removed the placeholders.
    expect(m).toMatch(/repoURL:\s*https:\/\/github\.com\/kubernetes-sigs\/gateway-api-inference-extension\b/);
    expect(m).toMatch(/path:\s*config\/crd\b/);
  });
  it('ai-gateway-prereqs.yaml does not prune the CRD-owning Applications', () => {
    // Pruning a CRD deletes every CR of that kind cluster-wide, so the two
    // CRD Applications must stay prune: false while the controller prunes.
    const docs = readExample('ai-gateway-prereqs.yaml').split(/^---$/m);
    // Anchored to end of line: a \b after "envoy-ai-gateway" also matches
    // inside "envoy-ai-gateway-crds", which would pick the wrong document.
    const appFor = (name) => docs.find((d) => new RegExp(`name: ${name}$`, 'm').test(d));
    expect(appFor('envoy-ai-gateway-crds')).toMatch(/prune:\s*false\b/);
    expect(appFor('gateway-api-inference-extension')).toMatch(/prune:\s*false\b/);
    expect(appFor('envoy-ai-gateway')).toMatch(/prune:\s*true\b/);
  });
  it('installation.md installs the pack from the published OCI chart (0.1.2)', () => {
    const page = readPage('installation.md');
    expect(page).toMatch(/repoURL:\s*quay\.io\/nebari\/charts\b/);
    expect(page).toMatch(/chart:\s*nebari-llm-serving\b/);
    expect(page).toMatch(/targetRevision:\s*"0\.1\.2"/);
    expect(page).not.toMatch(/path:\s*charts\/nebari-llm-serving\b/);
    expect(page).not.toMatch(/targetRevision:\s*v0\.1\.0-alpha\.9\b/);
  });
});
