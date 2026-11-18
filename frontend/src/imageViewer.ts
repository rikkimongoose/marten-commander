export const imageZoomSteps = Object.freeze([0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 4] as const);

export interface ImageLocation {
  protocol: string;
  host: string;
  hostname: string;
}

export function imageAssetURL(id: string, location?: ImageLocation): string {
  const path = `/viewer-image/${encodeURIComponent(id)}`;
  if (!location) return path;
  if (location.protocol === "wails:" || location.hostname === "wails.localhost") {
    return `${location.protocol}//${location.host}${path}`;
  }
  return path;
}

export function imageFitScale(naturalWidth: number, naturalHeight: number, viewportWidth: number, viewportHeight: number): number {
  if (naturalWidth <= 0 || naturalHeight <= 0 || viewportWidth <= 0 || viewportHeight <= 0) return 1;
  return Math.min(1, viewportWidth / naturalWidth, viewportHeight / naturalHeight);
}

export function adjacentImageZoom(scale: number, direction: -1 | 1): number {
  if (direction > 0) return imageZoomSteps.find(value => value > scale + Number.EPSILON) ?? imageZoomSteps[imageZoomSteps.length - 1];
  return [...imageZoomSteps].reverse().find(value => value < scale - Number.EPSILON) ?? imageZoomSteps[0];
}

export function imageZoomLabel(scale: number): string {
  return `${Math.round(scale * 100)}%`;
}
