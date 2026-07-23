import { describe, expect, it } from 'vitest';
import { applyFilter, counts } from './filter';
import type { Episode } from '$lib/episodes';

function ep(id: string, status: Episode['status'], title: string, file: string): Episode {
  return {
    id,
    title,
    sourceFilename: file,
    language: 'fa',
    status,
    hasMaster: status !== 'uploaded',
    uploadedAt: '2026-07-01T00:00:00Z'
  };
}

const list: Episode[] = [
  ep('ep_a', 'uploaded', 'اقتصاد دیجیتال', 'a_master.mp4'),
  ep('ep_b', 'processing', 'بحران آب', 'b_master.mp4'),
  ep('ep_c', 'ready', 'تحریم‌ها', 'c_master.mp4'),
  ep('ep_d', 'ready', 'سینمای مستقل', 'd_master.mp4'),
  ep('ep_e', 'failed', 'روایت مهاجرت', 'e_master.mp4')
];

describe('counts', () => {
  it('tallies all / processing(non-terminal) / ready / failed', () => {
    expect(counts(list)).toEqual({ all: 5, processing: 2, ready: 2, failed: 1 });
  });
});

describe('applyFilter', () => {
  it('filters by status chip', () => {
    expect(applyFilter(list, 'ready', '').map((e) => e.id)).toEqual(['ep_c', 'ep_d']);
    expect(applyFilter(list, 'failed', '').map((e) => e.id)).toEqual(['ep_e']);
    expect(applyFilter(list, 'processing', '').map((e) => e.id)).toEqual(['ep_a', 'ep_b']);
    expect(applyFilter(list, 'all', '').length).toBe(5);
  });

  it('searches title / filename / id, case-insensitively', () => {
    expect(applyFilter(list, 'all', 'EP_C').map((e) => e.id)).toEqual(['ep_c']);
    expect(applyFilter(list, 'all', 'd_master').map((e) => e.id)).toEqual(['ep_d']);
    expect(applyFilter(list, 'all', 'بحران').map((e) => e.id)).toEqual(['ep_b']);
  });

  it('combines chip and query', () => {
    expect(applyFilter(list, 'ready', 'سینما').map((e) => e.id)).toEqual(['ep_d']);
    expect(applyFilter(list, 'failed', 'ready').length).toBe(0);
  });
});
