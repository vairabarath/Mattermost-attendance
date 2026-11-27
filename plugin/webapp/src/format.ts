// Time formatting for the panel.

const pad = (n: number) => String(n).padStart(2, '0');

// clock: 3725 → "1:02:05"
export function clock(totalSeconds: number): string {
    const s = Math.max(0, Math.floor(totalSeconds));
    return `${Math.floor(s / 3600)}:${pad(Math.floor((s % 3600) / 60))}:${pad(s % 60)}`;
}

// hm: 3725 → "1h 2m" (as the bot writes durations)
export function hm(totalSeconds: number): string {
    const m = Math.max(0, Math.floor(totalSeconds / 60));
    return `${Math.floor(m / 60)}h ${m % 60}m`;
}

// timeOfDay: an ISO time → "9:12 AM" in the viewer's time zone.
export function timeOfDay(iso: string): string {
    return new Date(iso).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
}
