import type { HomeDirectory, Volume, VolumeSpace } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";

export function locationCaption(home: HomeDirectory | undefined, volume: Volume | undefined): string {
  if (home) return `Home — ${home.displayPath}`;
  return volume ? `${volume.name} — ${volume.displayPath}` : "Выбор места";
}

export function formatBytes(value: bigint): string {
  const units = ["Б", "КиБ", "МиБ", "ГиБ", "ТиБ", "ПиБ", "ЭиБ"];
  let unit = 0, scale = 1n;
  while (value >= scale * 1024n && unit < units.length - 1) { scale *= 1024n; unit++; }
  const tenths = (value * 10n + scale / 2n) / scale;
  return `${(Number(tenths) / 10).toLocaleString("ru-RU", { maximumFractionDigits: 1 })} ${units[unit]}`;
}

export function spacePresentation(space: VolumeSpace | null | undefined) {
  const unknown = { text: "Объём недоступен", percent: null, title: "Нет данных об объёме файловой системы" };
  if (!space) return unknown;
  try {
    if (![space.total, space.free, space.available, space.used].every(value => /^\d+$/.test(value))) return unknown;
    const total = BigInt(space.total), free = BigInt(space.free), available = BigInt(space.available), used = BigInt(space.used);
    if (total === 0n || free > total || available > free || used !== total - free) return unknown;
    return {
      text: `${formatBytes(available)} свободно из ${formatBytes(total)}`,
      percent: Number(used * 10000n / total) / 100,
      title: `Занято ${formatBytes(used)}. Свободно пользователю ${formatBytes(available)}; резерв/ограничения ОС ${formatBytes(free - available)}.`,
    };
  } catch { return unknown; }
}

type VolumeCall<T> = PromiseLike<T> & { cancel?: () => void | PromiseLike<void> };
export type VolumeChoices = {
  volumes: Volume[];
  home: HomeDirectory | null;
  errors: string[];
  loadingVolumes: boolean;
  loadingHome: boolean;
};

export function cancelVolumeCall(call: { cancel?: () => void | PromiseLike<void> } | undefined) {
  try {
    Promise.resolve(call?.cancel?.()).catch(error => console.warn("Cancel volume request:", error));
  } catch (error) {
    console.warn("Cancel volume request:", error);
  }
}

export async function loadVolumeChoices(
  list: () => VolumeCall<Volume[] | null>,
  home: () => VolumeCall<HomeDirectory>,
  onUpdate?: (choices: VolumeChoices) => void,
  signal?: AbortSignal,
): Promise<VolumeChoices> {
  let result: VolumeChoices = { volumes: [], home: null, errors: [], loadingVolumes: true, loadingHome: true };
  let listCall: VolumeCall<Volume[] | null> | undefined;
  let homeCall: VolumeCall<HomeDirectory> | undefined;
  const cancel = () => { cancelVolumeCall(listCall); cancelVolumeCall(homeCall); };
  const update = (patch: Partial<VolumeChoices>) => {
    if (signal?.aborted) return;
    result = { ...result, ...patch };
    onUpdate?.(result);
  };
  const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error);
  if (signal?.aborted) return { ...result, loadingVolumes: false, loadingHome: false };
  signal?.addEventListener("abort", cancel, { once: true });
  try {
    // Publish each result independently: a stalled drive must not hide Home.
    await Promise.all([
      (async () => {
        try {
          listCall = list();
          update({ volumes: await listCall ?? [], loadingVolumes: false });
        } catch (error) {
          update({ errors: [...result.errors, errorMessage(error)], loadingVolumes: false });
        }
      })(),
      (async () => {
        try {
          homeCall = home();
          update({ home: await homeCall, loadingHome: false });
        } catch (error) {
          update({ errors: [...result.errors, errorMessage(error)], loadingHome: false });
        }
      })(),
    ]);
    return result;
  } finally {
    signal?.removeEventListener("abort", cancel);
  }
}
