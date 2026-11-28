import {act as reactAct, render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from 'api';
import type {Me} from 'api';
import React from 'react';
import {Provider} from 'react-redux';
import {createStore} from 'redux';

import {greeting} from './Home';
import Panel from './Panel';
import {requestRefresh} from './refresh';

jest.mock('api');
const mocked = api as jest.Mocked<typeof api>;

const TEAM = 'tttttttttttttttttttttttttt';

function me(over: Partial<Me> = {}): Me {
    return {
        now: '2026-11-27T04:00:00Z',
        user_id: 'u',
        team: {id: TEAM, name: 'Bittrix Technologies'},
        is_admin: false,
        github_linked: true,
        github_login: 'vairabarath',
        github_set_up: true,
        display_name: 'Barath Kumar',
        eod: {available: false, submitted: false},
        session: null,
        today_team_minutes: 0,
        today_all_minutes: 0,
        today_team_seconds: 0,
        today_all_seconds: 0,
        ...over,
    };
}

const working = (over: Partial<NonNullable<Me['session']>> = {}): Me['session'] => ({
    team: {id: TEAM, name: 'Bittrix Technologies'},
    check_in: '2026-11-27T03:30:00Z',
    mode: 'office',
    on_break: false,
    net_minutes: 62,
    net_seconds: 3725,
    ...over,
});

const eodForm = (): api.EODForm => ({
    available: true,
    team: {id: TEAM, name: 'Bittrix Technologies'},
    day: '2026-11-27',
    day_label: 'Fri 27 Nov',
    statuses: ['In progress', 'In review', 'Blocked', 'Done'],
    no_change: 'no_change',
    tasks: [],
    done: [],
    others: [],
    did: '',
    blockers: '',
    tomorrow: '',
    min_did: 10,
    max_text: 2000,
    submitted: false,
    state: '{}',
});

function renderPanel(teamId = TEAM) {
    const store = createStore(() => ({entities: {teams: {currentTeamId: teamId}}}));
    return render(<Provider store={store}><Panel/></Provider>);
}

const tile = (name: string) => screen.getByRole('button', {name: new RegExp(`^${name}`)});

beforeEach(() => {
    window.sessionStorage.clear();
    mocked.fetchTasks.mockResolvedValue({configured: true,
        linked: true,
        tasks: [
            {number: 7, title: 'A', url: 'u7', repo: 'o/ops', status: 'Blocked', overdue: true, off_board: false, picked: true},
            {number: 9, title: 'B', url: 'u9', repo: 'o/ops', status: 'Todo', overdue: false, off_board: true, picked: false},
        ]});
    mocked.fetchWFH.mockResolvedValue({team: {id: TEAM, name: 'B'}, today: '2026-11-27', pending: 1, requests: []});
    mocked.fetchHours.mockResolvedValue({range: 'week', label: '', days: [], team_seconds: 0, all_seconds: 75600, by_org: [], has_expected: true, expected_seconds: 86400, worked_until_yesterday: 61200, per_day_hours: 8});
    mocked.fetchEOD.mockResolvedValue(eodForm());
});
afterEach(() => jest.useRealTimers());

test('greeting', () => {
    expect(greeting(9)).toBe('Good morning');
    expect(greeting(13)).toBe('Good afternoon');
    expect(greeting(19)).toBe('Good evening');
});

test('home: greeting, status bar and tiles with live summaries', async () => {
    mocked.fetchMe.mockResolvedValue(me({eod: {available: true, submitted: false, day_label: 'Fri 27 Nov'}}));
    renderPanel();

    expect(await screen.findByRole('heading', {name: /, Barath$/})).toBeInTheDocument();
    expect(screen.getByLabelText('Status')).toHaveTextContent('Not checked in');
    expect(await screen.findByText('1 today · 2 open · 1 🆕')).toBeInTheDocument();
    expect(screen.getByText('1 pending')).toBeInTheDocument();
    expect(screen.getByText('21h 0m this week · 71% of expected')).toBeInTheDocument();
    expect(tile('End of day')).toHaveTextContent('Not sent yet · Fri 27 Nov');
    expect(tile('My settings')).toHaveTextContent('GitHub ✓ vairabarath');
    expect(mocked.fetchMe).toHaveBeenCalledWith(TEAM);
});

test('home: the greeting capitalizes a lowercase name', async () => {
    mocked.fetchMe.mockResolvedValue(me({display_name: 'barath'}));
    renderPanel();
    expect(await screen.findByRole('heading', {name: /, Barath$/})).toBeInTheDocument();
});

test('home: no Tasks tile where GitHub isn’t set up', async () => {
    mocked.fetchMe.mockResolvedValue(me({github_set_up: false}));
    renderPanel();
    await screen.findByLabelText('Status');
    expect(screen.queryByRole('button', {name: /Tasks/})).not.toBeInTheDocument();
    expect(mocked.fetchTasks).not.toHaveBeenCalled();
});

test('home status bar: check in, then check out (confirmed) goes to the end-of-day report', async () => {
    mocked.fetchMe.mockResolvedValue(me());
    mocked.act.mockResolvedValue({message: '✅ Checked in to **Bittrix Technologies**', me: me({session: working({net_seconds: 0})})});
    renderPanel();

    await userEvent.click(await screen.findByText('▶ Check in'));
    expect(mocked.act).toHaveBeenCalledWith(TEAM, 'in', '');
    expect(await within(screen.getByLabelText('Status')).findByText(/^Working/)).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Checked in to Bittrix Technologies');

    mocked.act.mockResolvedValue({message: '🔴 Checked out', me: me()});
    await userEvent.click(screen.getByText('Check out'));
    expect(mocked.act).toHaveBeenCalledTimes(1); // asks first
    await userEvent.click(screen.getByText('Yes, check out'));
    expect(mocked.act).toHaveBeenLastCalledWith(TEAM, 'out', '');
    expect(await screen.findByRole('heading', {name: '📝 End of day'})).toBeInTheDocument();
    expect(await screen.findByRole('form', {name: 'End-of-day report'})).toBeInTheDocument();
});

test('navigation: tiles open pages, ← Home goes back, the page is remembered', async () => {
    mocked.fetchMe.mockResolvedValue(me({session: working()}));
    const first = renderPanel();

    await userEvent.click(await screen.findByRole('button', {name: /^Attendance/}));
    expect(screen.getByRole('heading', {name: '⏱ Attendance'})).toBeInTheDocument();
    expect(screen.getByLabelText('Time worked this session')).toBeInTheDocument();
    first.unmount();

    renderPanel(); // reopening the panel: still on Attendance
    expect(await screen.findByRole('heading', {name: '⏱ Attendance'})).toBeInTheDocument();
    await userEvent.click(screen.getByText('← Home'));
    expect(await screen.findByLabelText('Status')).toBeInTheDocument();
    expect(screen.queryByText('← Home')).not.toBeInTheDocument();
});

test('attendance page: live timer, break, check out with a confirmation', async () => {
    jest.useFakeTimers({now: Date.parse('2026-11-27T04:32:05Z'), advanceTimers: true});
    window.sessionStorage.setItem('attendance.page', 'attendance');
    mocked.fetchMe.mockResolvedValue(me({session: working(), today_team_seconds: 3725, today_all_seconds: 3725}));
    renderPanel();

    expect(await screen.findByLabelText('Time worked this session')).toHaveTextContent('1:02:05');
    reactAct(() => {
        jest.advanceTimersByTime(3000);
    });
    expect(screen.getByLabelText('Time worked this session')).toHaveTextContent('1:02:08');
    expect(screen.getByLabelText('Today')).toHaveTextContent('Today 1h 2m');

    mocked.act.mockResolvedValue({message: '☕ On break', me: me({session: working({on_break: true, break_since: new Date().toISOString()})})});
    await userEvent.click(screen.getByText('☕ Break'));
    expect(await screen.findByText('▶ Back')).toBeInTheDocument();
    const before = screen.getByLabelText('Time worked this session').textContent;
    reactAct(() => {
        jest.advanceTimersByTime(5000);
    });
    expect(screen.getByLabelText('Time worked this session').textContent).toBe(before); // stands still

    await userEvent.click(screen.getByText('🔴 Check out'));
    await userEvent.click(screen.getByText('Cancel'));
    expect(mocked.act).toHaveBeenCalledTimes(1);
});

test('attendance page: check in with a note; checked in to another team', async () => {
    window.sessionStorage.setItem('attendance.page', 'attendance');
    mocked.fetchMe.mockResolvedValue(me());
    mocked.act.mockResolvedValue({message: 'ok', me: me({session: working({team: {id: 'otherteamotherteamotherte', name: 'InkYank'}})})});
    renderPanel();
    await userEvent.click(await screen.findByText('+ Add a note'));
    await userEvent.type(screen.getByLabelText('Check-in note'), 'standup at 10');
    await userEvent.click(screen.getByText('▶ Check in'));
    expect(mocked.act).toHaveBeenCalledWith(TEAM, 'in', 'standup at 10');
    expect(await screen.findByText(/in InkYank/)).toBeInTheDocument();
});

test('errors: not an attendance team, retry', async () => {
    mocked.fetchMe.mockRejectedValueOnce(new Error('Attendance isn’t used in this team.'));
    renderPanel();
    expect(await screen.findByRole('alert')).toHaveTextContent('Attendance isn’t used in this team.');
    mocked.fetchMe.mockResolvedValue(me());
    await userEvent.click(screen.getByText('Try again'));
    expect(await screen.findByLabelText('Status')).toBeInTheDocument();
});

test('live events: a check-out elsewhere updates the status and clears the old reply; others don’t reload it', async () => {
    mocked.fetchMe.mockResolvedValue(me());
    mocked.act.mockResolvedValue({message: '✅ Checked in', me: me({session: working()})});
    renderPanel();
    await userEvent.click(await screen.findByText('▶ Check in'));
    expect(await screen.findByRole('status')).toHaveTextContent('Checked in');

    mocked.fetchMe.mockResolvedValue(me());
    reactAct(() => {
        requestRefresh('session_changed');
    });
    expect(await within(await screen.findByLabelText('Status')).findByText('Not checked in')).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();

    // (A check-out also refreshes the end-of-day tile: let that settle.)
    await new Promise((r) => setTimeout(r, 400));
    const calls = mocked.fetchMe.mock.calls.length;
    reactAct(() => {
        requestRefresh('tasks_changed');
    });
    await new Promise((r) => setTimeout(r, 400));
    expect(mocked.fetchMe).toHaveBeenCalledTimes(calls);
});

test('the end-of-day report is told about check-in/out, not about breaks', async () => {
    window.sessionStorage.setItem('attendance.page', 'attendance');
    const seen: string[] = [];
    const listen = (e: Event) => seen.push((e as CustomEvent).detail?.event);
    window.addEventListener('attendance:refresh', listen);
    try {
        mocked.fetchMe.mockResolvedValue(me({session: working()}));
        renderPanel();
        expect(await screen.findByText('Working')).toBeInTheDocument();

        mocked.act.mockResolvedValue({message: '☕', me: me({session: working({on_break: true, break_since: new Date().toISOString()})})});
        await userEvent.click(screen.getByText('☕ Break'));
        expect(await screen.findByText('On break')).toBeInTheDocument();
        expect(seen).not.toContain('eod_changed');

        mocked.act.mockResolvedValue({message: '🔴', me: me()});
        await userEvent.click(screen.getByText('🔴 Check out'));
        await userEvent.click(screen.getByText('Yes, check out'));
        await screen.findByRole('heading', {name: '📝 End of day'});
        expect(seen).toContain('eod_changed');
    } finally {
        window.removeEventListener('attendance:refresh', listen);
    }
});
