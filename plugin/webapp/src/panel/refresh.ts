// Live refresh inside the panel. The plugin turns server events into a
// window event carrying what changed; each section reloads only for the
// events that affect it, at most once per burst.

import {useEffect, useRef} from 'react';

export const REFRESH_EVENT = 'attendance:refresh';

// ALL: reload everything (reconnect, window shown again, safety poll).
export const ALL = 'all';

export type RefreshDetail = {event: string; teamId?: string};

// requestRefresh tells the panel's sections that `event` happened.
export function requestRefresh(event: string = ALL, teamId?: string) {
    window.dispatchEvent(new CustomEvent<RefreshDetail>(REFRESH_EVENT, {detail: {event, teamId}}));
}

const DEBOUNCE_MS = 250;

// useRefresh calls reload when one of `events` (or ALL) happens. Events
// arriving within DEBOUNCE_MS of each other cause one reload. A plain
// REFRESH_EVENT without detail counts as ALL.
export function useRefresh(events: string[], reload: () => void, enabled = true) {
    const reloadRef = useRef(reload);
    reloadRef.current = reload;
    const key = events.join(',');

    useEffect(() => {
        if (!enabled) {
            return undefined;
        }
        const wanted = new Set(key.split(','));
        let timer: number | undefined;
        const onEvent = (e: Event) => {
            const ev = (e as CustomEvent<RefreshDetail>).detail?.event ?? ALL;
            if (ev !== ALL && !wanted.has(ev)) {
                return;
            }
            window.clearTimeout(timer);
            timer = window.setTimeout(() => reloadRef.current(), DEBOUNCE_MS);
        };
        window.addEventListener(REFRESH_EVENT, onEvent);
        return () => {
            window.removeEventListener(REFRESH_EVENT, onEvent);
            window.clearTimeout(timer);
        };
    }, [key, enabled]);
}
