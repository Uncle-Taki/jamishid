import type { MarketPlatform, NormalizedVehicleListing, ScoreWeights, ScoredListing, VehicleType } from '../types/market';
import { calculateUtilityScores } from '../utils/utilityScorer';

export interface SearchCriteria {
	keywords?: string;
  bamaCategory?: string;
  cursors?: Partial<Record<MarketPlatform, string>>;
  query: string;
  citySlug: string;
  type: VehicleType;
  platform?: MarketPlatform;
  dateCutoffHours?: number;
  weights?: ScoreWeights;
}

export interface SourceStatus {
  nextCursor?: string;
  page?: number;
  platform: MarketPlatform;
  status: 'ok' | 'unavailable';
  count: number;
  error?: string;
}

export interface CrawlResponse {
  success: boolean;
  data: ScoredListing[];
  sources: SourceStatus[];
  cached: boolean;
  error?: string;
}

export class CrawlError extends Error {
  constructor(message: string, public sources: SourceStatus[] = []) {
    super(message);
    this.name = 'CrawlError';
  }
}

export async function fetchLiveListings(
  criteria: SearchCriteria,
  signal?: AbortSignal,
  fetcher: typeof fetch = fetch,
): Promise<CrawlResponse> {
  const base = (import.meta.env?.VITE_API_URL || '/api/v1').replace(/\/$/, '');
  const timeout = AbortSignal.timeout(25_000);
  const response = await fetcher(`${base}/crawl`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(criteria),
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  let payload: CrawlResponse;
  try {
    payload = await response.json();
  } catch {
    throw new CrawlError('پاسخ نامعتبر از سرور دریافت شد.');
  }
  if (!response.ok || !payload.success) {
    throw new CrawlError('دریافت آگهی‌ها از منابع بازار انجام نشد. دوباره تلاش کنید.', payload.sources || []);
  }
  if (!Array.isArray(payload.data) || !Array.isArray(payload.sources)) {
    throw new CrawlError('ساختار پاسخ سرور معتبر نیست.');
  }
  return payload;
}

export interface Category { id: string; label: string; parentId?: string; type: VehicleType }

export interface CrawlJob {
  retryAt?: string; retriesPending?: number;
  detailsPending?: number; detailsFailed?: number; crawlDone?: boolean;
  id: string; status: string; done: boolean; criteria: SearchCriteria;
  sources: SourceStatus[]; data: NormalizedVehicleListing[]; truncated: boolean;
  newCount: number; changedCount: number;
}
const apiBase = () => (import.meta.env?.VITE_API_URL || '/api/v1').replace(/\/$/, '');

class TemporaryJobError extends CrawlError {}

async function jobRequest(path: string, signal?: AbortSignal, body?: unknown): Promise<CrawlJob> {
  const timeout = AbortSignal.timeout(10000);
  try {
    const response = await fetch(`${apiBase()}${path}`, { method: body ? 'POST' : 'GET',
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
      signal: signal ? AbortSignal.any([signal, timeout]) : timeout });
    if (!response.ok) {
      const ErrorType = response.status === 408 || response.status === 429 || response.status >= 500 ? TemporaryJobError : CrawlError;
      throw new ErrorType('سرویس جستجوی پس‌زمینه در دسترس نیست. دوباره تلاش کنید.');
    }
    const job = await response.json();
    if (!job?.id || !Array.isArray(job.data) || !Array.isArray(job.sources) || typeof job.done !== 'boolean') throw new CrawlError('پاسخ وضعیت جستجو معتبر نیست.');
    return job;
  } catch (error) {
    signal?.throwIfAborted();
    if (timeout.aborted || error instanceof TypeError || (error instanceof Error && error.name === 'TimeoutError')) {
      throw new TemporaryJobError('ارتباط با سرور جستجو برقرار نشد. دوباره تلاش کنید.');
    }
    if (error instanceof SyntaxError) throw new CrawlError('پاسخ وضعیت جستجو معتبر نیست.');
    throw error;
  }
}
export const createCrawlJob = (criteria: SearchCriteria, signal?: AbortSignal, statusOnly = false) => jobRequest(statusOnly ? '/jobs?statusOnly=1' : '/jobs', signal, criteria);
export const getCrawlJob = (id: string, signal?: AbortSignal, statusOnly = false) => jobRequest(`/jobs/${encodeURIComponent(id)}${statusOnly ? '?statusOnly=1' : ''}`, signal);
export const changeCrawlJob = (id: string, action: 'pause' | 'resume' | 'retry', platform?: MarketPlatform) =>
  jobRequest(`/jobs/${encodeURIComponent(id)}/actions`, undefined, { action, platform });

export async function watchCrawlJob(job: CrawlJob, signal: AbortSignal, progress: (job: CrawlJob) => void, statusOnly = false): Promise<void> {
  signal.throwIfAborted();
  progress(job);
  let failures = 0;
  while (!signal.aborted) {
    if (job.done) return;
    await new Promise<void>((resolve, reject) => {
      const abort = () => { clearTimeout(timer); signal.removeEventListener('abort', abort); reject(signal.reason); };
      const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve(); }, failures ? 1000 * 2 ** failures : statusOnly ? 2000 : 1000);
      signal.addEventListener('abort', abort, { once: true });
      if (signal.aborted) abort();
    });
    try {
      job = await getCrawlJob(job.id, signal, statusOnly);
      signal.throwIfAborted();
      failures = 0;
      progress(job);
    } catch (error) {
      signal.throwIfAborted();
      if (!(error instanceof TemporaryJobError)) throw error;
      if (++failures > 4) {
        throw new CrawlError('دریافت وضعیت جستجو پس از چند تلاش انجام نشد؛ نتایج فعلی محفوظ‌اند. جستجو ممکن است روی سرور ادامه داشته باشد؛ برای دریافت وضعیت دوباره تلاش کنید.');
      }
    }
  }
  signal.throwIfAborted();
}

export function jobListings(job: CrawlJob): ScoredListing[] { return calculateUtilityScores(job.data); }

export async function fetchStoredInventory(criteria: SearchCriteria, offset = 0, signal?: AbortSignal): Promise<{ data: ScoredListing[]; total: number }> {
  const params = new URLSearchParams({ q: criteria.keywords || '', model: criteria.query, type: criteria.type, city: criteria.citySlug,
    category: criteria.bamaCategory || '', limit: '100', offset: String(offset), sort: 'relevance' });
  const response = await fetch(`${apiBase()}/inventory?${params}`, { signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(10000)]) : AbortSignal.timeout(10000) });
  if (!response.ok) throw new CrawlError('دریافت آگهی‌های ذخیره‌شده انجام نشد.');
  const result = await response.json();
  if (!Array.isArray(result.data) || typeof result.total !== 'number') throw new CrawlError('پاسخ آگهی‌های ذخیره‌شده معتبر نیست.');
  return { data: calculateUtilityScores(result.data), total: result.total };
}

export async function fetchListingDetails(listing: NormalizedVehicleListing, signal?: AbortSignal, fetcher: typeof fetch = fetch): Promise<Partial<NormalizedVehicleListing>> {
  const base = (import.meta.env?.VITE_API_URL || '/api/v1').replace(/\/$/, '');
  const params = new URLSearchParams({ platform: listing.platform, url: listing.url });
  const response = await fetcher(`${base}/listing?${params}`, { signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(12000)]) : AbortSignal.timeout(12000) });
  if (!response.ok) throw new Error('Listing details unavailable');
  const { data } = await response.json();
  if (!data || data.detailsStatus !== 'complete' || !Array.isArray(data.attributes)) throw new Error('Invalid listing details');
  const facts: Partial<NormalizedVehicleListing> = { attributes: data.attributes, detailsStatus: 'complete' };
  if (data.mileageKm >= 0) facts.mileageKm = data.mileageKm;
  if (data.year > 0) facts.year = data.year;
  if (data.priceToman > 0) facts.priceToman = data.priceToman;
  if (data.bodyStatus && data.bodyStatus !== 'unknown') facts.bodyStatus = data.bodyStatus;
  for (const field of ['colorFa', 'gearbox', 'fuelType', 'engineVolumeCc', 'technicalStatus', 'documentStatus', 'brandModel', 'trimName'] as const) {
    if (data[field]) Object.assign(facts, { [field]: data[field] });
  }
  return facts;
}
export async function fetchCategories(type: VehicleType, parent?: string, signal?: AbortSignal): Promise<Category[]> {
 const base=(import.meta.env?.VITE_API_URL || '/api/v1').replace(/\/$/, '');
 const params=new URLSearchParams({type});if(parent)params.set('parent',parent);
 const response=await fetch(`${base}/categories?${params}`,{signal});
 if(!response.ok)throw new Error('Category catalog unavailable');
 const result=await response.json();if(!Array.isArray(result.data))throw new Error('Invalid catalog');
 return result.data;
}

export interface ListingHistory { listing: NormalizedVehicleListing; events: { observationId: number; observedAt: string; kind: string; statusCode: number; errorKind: string; data: NormalizedVehicleListing }[]; offset: number }
export async function fetchListingHistory(listing: NormalizedVehicleListing, offset = 0, signal?: AbortSignal): Promise<ListingHistory> {
  const response = await fetch(`${apiBase()}/inventory/${listing.platform}/${encodeURIComponent(listing.id)}/history?offset=${offset}`, { signal });
  if (!response.ok) throw new Error('دریافت سابقه انجام نشد.');
  return response.json();
}
export async function recheckListing(listing: NormalizedVehicleListing): Promise<void> {
  const response = await fetch(`${apiBase()}/inventory/${listing.platform}/${encodeURIComponent(listing.id)}/recheck`, { method: 'POST' });
  if (!response.ok) throw new Error('ثبت بررسی مجدد انجام نشد.');
}

export interface CoverageSource {
  platform: string; type: string; partitions: number; complete: number; overdue: number; active: number;
  lastDiscovery: string | null; catalogError: string;
}
export async function fetchCoverage(signal?: AbortSignal): Promise<CoverageSource[]> {
  const response = await fetch(`${apiBase()}/coverage`, { signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(5000)]) : AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error('Coverage unavailable');
  const result = await response.json();
  if (!Array.isArray(result.sources)) throw new Error('Invalid coverage');
  return result.sources;
}
