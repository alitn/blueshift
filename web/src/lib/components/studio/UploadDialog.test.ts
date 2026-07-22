import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

// Mock the episode client so the dialog flow is exercised without real fetch /
// XHR. contentTypeFor gates on extension; uploadMaster stands in for the
// create -> PUT -> complete round trip.
vi.mock('$lib/episodes', () => ({
  uploadMaster: vi.fn(
    async (_f: File, _t: string, onProgress: (p: string, f: number) => void) => {
      onProgress('uploading', 1);
      return 'ep_new';
    }
  ),
  contentTypeFor: (f: File) => (f.name.endsWith('.mp4') ? 'video/mp4' : null),
  MAX_MASTER_BYTES: 40 * 1024 * 1024 * 1024
}));

import UploadDialog from './UploadDialog.svelte';
import { uploadMaster } from '$lib/episodes';

afterEach(() => vi.clearAllMocks());

function mp4(name = 'interview.mp4') {
  return new File(['x'.repeat(10)], name, { type: 'video/mp4' });
}

describe('UploadDialog', () => {
  it('runs the upload flow and reports the new episode id', async () => {
    const onUploaded = vi.fn();
    render(UploadDialog, { props: { open: true, onUploaded } });
    const user = userEvent.setup();

    const input = screen.getByLabelText('File') as HTMLInputElement;
    await user.upload(input, mp4('interview.mp4'));

    // Title auto-fills from the filename (without extension).
    const title = screen.getByLabelText('Title') as HTMLInputElement;
    await waitFor(() => expect(title.value).toBe('interview'));

    await user.click(screen.getByRole('button', { name: 'UPLOAD' }));

    await waitFor(() => expect(uploadMaster).toHaveBeenCalledTimes(1));
    const [, passedTitle] = vi.mocked(uploadMaster).mock.calls[0];
    expect(passedTitle).toBe('interview');
    expect(onUploaded).toHaveBeenCalledWith('ep_new');
  });

  it('rejects an oversized file with a neutral message and no upload', async () => {
    render(UploadDialog, { props: { open: true, onUploaded: vi.fn() } });
    const user = userEvent.setup();

    const big = mp4('huge.mp4');
    // A valid-type file that exceeds the 40 GB cap trips the size guard.
    Object.defineProperty(big, 'size', { value: 41 * 1024 * 1024 * 1024 });

    const input = screen.getByLabelText('File') as HTMLInputElement;
    await user.upload(input, big);

    expect(await screen.findByRole('alert')).toHaveTextContent(/40 GB/i);
    expect(uploadMaster).not.toHaveBeenCalled();
    // UPLOAD stays disabled with no valid file.
    expect(screen.getByRole('button', { name: 'UPLOAD' })).toBeDisabled();
  });
});
