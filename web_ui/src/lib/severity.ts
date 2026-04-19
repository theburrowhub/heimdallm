const CLASSES: Record<string, string> = {
  critical: 'bg-red-100 text-red-700',
  high: 'bg-orange-100 text-orange-700',
  medium: 'bg-amber-100 text-amber-700',
  low: 'bg-gray-100 text-gray-600'
};

const ORDER: Record<string, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1
};

export function severityClass(sev: string): string {
  return CLASSES[sev.toLowerCase()] ?? 'bg-gray-100 text-gray-600';
}

export function severityOrder(sev: string): number {
  return ORDER[sev.toLowerCase()] ?? 0;
}
