/**
 * Client for the neutral episode endpoints (list, create, upload-complete,
 * proxy, retry). The browser only ever sees Blueshift-neutral fields and
 * opaque, prefixed ids — nothing here names the underlying stack. Error copy is
 * deliberately generic.
 */

/** Non-terminal statuses keep the Library polling; terminal ones stop it. */
export type EpisodeStatus = 'uploaded' | 'processing' | 'ready' | 'failed';

/** Episode is the camelCase view of the list DTO the UI renders. */
export type Episode = {
  id: string;
  title: string;
  sourceFilename: string;
  language: string;
  status: EpisodeStatus;
  durationMs?: number;
  sizeBytes?: number;
  uploadedAt: string;
};

/** The raw list DTO as returned by GET /api/episodes (snake_case). */
type EpisodeDTO = {
  id: string;
  title: string;
  source_filename: string;
  language: string;
  status: EpisodeStatus;
  duration_ms?: number;
  size_bytes?: number;
  uploaded_at: string;
};

type UploadInstructions = { url: string; method: string; headers?: Record<string, string> };

function fromDTO(d: EpisodeDTO): Episode {
  return {
    id: d.id,
    title: d.title,
    sourceFilename: d.source_filename,
    language: d.language,
    status: d.status,
    durationMs: d.duration_ms,
    sizeBytes: d.size_bytes,
    uploadedAt: d.uploaded_at
  };
}

/** listEpisodes fetches the org's episodes newest-first. Throws on failure. */
export async function listEpisodes(): Promise<Episode[]> {
  const res = await fetch('/api/episodes', { credentials: 'same-origin' });
  if (!res.ok) throw new Error('list_failed');
  const body = (await res.json()) as { episodes: EpisodeDTO[] };
  return body.episodes.map(fromDTO);
}

/** A terminal episode no longer changes state, so polling can stop for it. */
export function isTerminal(status: EpisodeStatus): boolean {
  return status === 'ready' || status === 'failed';
}

/** retryEpisode re-drives a failed episode. Resolves true on success. */
export async function retryEpisode(id: string): Promise<boolean> {
  const res = await fetch(`/api/episodes/${encodeURIComponent(id)}/retry`, {
    method: 'POST',
    credentials: 'same-origin'
  });
  return res.ok;
}

/** fetchProxyUrl returns a short-lived signed URL for a Ready episode's proxy. */
export async function fetchProxyUrl(id: string): Promise<string | null> {
  const res = await fetch(`/api/episodes/${encodeURIComponent(id)}/proxy`, {
    credentials: 'same-origin'
  });
  if (!res.ok) return null;
  const body = (await res.json()) as { url: string; expires_at: string };
  return body.url;
}

export type CreateInput = {
  title: string;
  sourceFilename: string;
  sizeBytes: number;
  contentType: string;
};

type CreateResponse = { episode: EpisodeDTO; upload: UploadInstructions };

/** createEpisode registers the row and returns the id + upload instructions. */
async function createEpisode(
  input: CreateInput
): Promise<{ id: string; upload: UploadInstructions }> {
  const res = await fetch('/api/episodes', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      title: input.title,
      source_filename: input.sourceFilename,
      size_bytes: input.sizeBytes,
      content_type: input.contentType
    })
  });
  if (!res.ok) throw new Error('create_failed');
  const body = (await res.json()) as CreateResponse;
  return { id: body.episode.id, upload: body.upload };
}

/**
 * putWithProgress transfers the master bytes to the signed URL, reporting
 * progress in [0,1]. It uses XMLHttpRequest because fetch cannot report upload
 * progress; the request shape (single PUT with headers) matches both blob
 * backends.
 */
function putWithProgress(
  upload: UploadInstructions,
  file: Blob,
  onProgress: (fraction: number) => void
): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open(upload.method, upload.url, true);
    for (const [k, v] of Object.entries(upload.headers ?? {})) {
      xhr.setRequestHeader(k, v);
    }
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.total ? e.loaded / e.total : 0);
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve();
      else reject(new Error('upload_failed'));
    };
    xhr.onerror = () => reject(new Error('upload_failed'));
    xhr.send(file);
  });
}

/** completeUpload verifies the transfer and launches ingest. */
async function completeUpload(id: string): Promise<void> {
  const res = await fetch(`/api/episodes/${encodeURIComponent(id)}/upload-complete`, {
    method: 'POST',
    credentials: 'same-origin'
  });
  if (!res.ok) throw new Error('complete_failed');
}

/** The MIME type for each accepted master container. */
export const CONTENT_TYPES: Record<string, string> = {
  mp4: 'video/mp4',
  mov: 'video/quicktime',
  mxf: 'application/mxf'
};

/** 40 GB cap, matching the API and the dialog copy. */
export const MAX_MASTER_BYTES = 40 * 1024 * 1024 * 1024;

/** contentTypeFor resolves a file's container MIME from its extension. */
export function contentTypeFor(file: File): string | null {
  if (file.type && Object.values(CONTENT_TYPES).includes(file.type)) return file.type;
  const ext = file.name.split('.').pop()?.toLowerCase() ?? '';
  return CONTENT_TYPES[ext] ?? null;
}

export type UploadPhase = 'creating' | 'uploading' | 'finalizing';

/**
 * uploadMaster runs the full create -> PUT -> complete flow, reporting progress.
 * On success the new episode is 'uploaded' and the worker has been triggered;
 * the caller refreshes the list and lets the poll store watch it advance.
 */
export async function uploadMaster(
  file: File,
  title: string,
  onProgress: (phase: UploadPhase, fraction: number) => void
): Promise<string> {
  const contentType = contentTypeFor(file);
  if (!contentType) throw new Error('unsupported_type');
  onProgress('creating', 0);
  const { id, upload } = await createEpisode({
    title,
    sourceFilename: file.name,
    sizeBytes: file.size,
    contentType
  });
  onProgress('uploading', 0);
  await putWithProgress(upload, file, (f) => onProgress('uploading', f));
  onProgress('finalizing', 1);
  await completeUpload(id);
  return id;
}
