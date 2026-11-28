// What the status card and the Home status bar share: the user's state and
// how to run a session action.

import type {Me, SessionAction} from 'api';

export type SessionProps = {
    me: Me;
    fetchedAt: number; // when `me` was loaded (the timer ticks on from there)
    busy: SessionAction | '';
    run: (action: SessionAction, text?: string) => void;
};

// liveSeconds adds the time since the last load to a running session.
export function liveSeconds(p: SessionProps, now: number) {
    const s = p.me.session;
    const ticking = Boolean(s && !s.on_break);
    const since = ticking && p.fetchedAt ? Math.max(0, (now - p.fetchedAt) / 1000) : 0;
    const elsewhere = Boolean(s && s.team.id !== p.me.team.id);
    return {
        work: s ? s.net_seconds + since : 0,
        today: p.me.today_team_seconds + (elsewhere ? 0 : since),
        all: p.me.today_all_seconds + since,
        breakSeconds: s?.on_break && s.break_since ? (now - Date.parse(s.break_since)) / 1000 : 0,
        elsewhere,
    };
}

export function sessionState(me: Me): 'on' | 'break' | 'off' {
    if (me.session?.on_break) {
        return 'break';
    }
    return me.session ? 'on' : 'off';
}
