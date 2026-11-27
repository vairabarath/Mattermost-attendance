import {act as reactAct, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from 'api';
import type {Me} from 'api';
import React from 'react';
import {Provider} from 'react-redux';
import {createStore} from 'redux';

import Panel, {REFRESH_EVENT} from './Panel';

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

function renderPanel(teamId = TEAM) {
    const store = createStore(() => ({entities: {teams: {currentTeamId: teamId}}}));
    return render(<Provider store={store}><Panel/></Provider>);
}

afterEach(() => jest.useRealTimers());

test('not checked in: check in with a note', async () => {
    mocked.fetchMe.mockResolvedValue(me());
    mocked.act.mockResolvedValue({message: '✅ Checked in to **Bittrix Technologies** at 9:00 AM', me: me({session: working({net_seconds: 0})})});
    renderPanel();

    expect(await screen.findByText('Not checked in')).toBeInTheDocument();
    expect(mocked.fetchMe).toHaveBeenCalledWith(TEAM);
    await userEvent.click(screen.getByText('+ Add a note'));
    await userEvent.type(screen.getByLabelText('Check-in note'), 'standup at 10');
    await userEvent.click(screen.getByText('▶ Check in'));

    expect(mocked.act).toHaveBeenCalledWith(TEAM, 'in', 'standup at 10');
    expect(await screen.findByText('Working')).toBeInTheDocument();

    // The bot's message, markdown markers stripped in the plain fallback.
    expect(screen.getByRole('status')).toHaveTextContent('✅ Checked in to Bittrix Technologies at 9:00 AM');
});

test('working: live timer, break, and check out needs a confirmation', async () => {
    jest.useFakeTimers({now: Date.parse('2026-11-27T04:32:05Z')});
    mocked.fetchMe.mockResolvedValue(me({session: working(), today_team_seconds: 3725, today_all_seconds: 3725}));
    renderPanel();

    expect(await screen.findByLabelText('Time worked this session')).toHaveTextContent('1:02:05');
    reactAct(() => {
        jest.advanceTimersByTime(3000);
    });
    expect(screen.getByLabelText('Time worked this session')).toHaveTextContent('1:02:08');
    expect(screen.getByLabelText('Today')).toHaveTextContent('Today here 1h 2m');
    jest.useRealTimers();

    mocked.act.mockResolvedValue({message: '☕ On break', me: me({session: working({on_break: true, break_since: new Date().toISOString()})})});
    await userEvent.click(screen.getByText('☕ Break'));
    expect(mocked.act).toHaveBeenLastCalledWith(TEAM, 'break', '');
    expect(await screen.findByText('▶ Back')).toBeInTheDocument();
    expect(screen.getByText('On break')).toBeInTheDocument();

    // One click doesn't check out: it asks first.
    await userEvent.click(screen.getByText('🔴 Check out'));
    expect(mocked.act).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByText('Cancel'));
    expect(screen.getByText('🔴 Check out')).toBeInTheDocument();

    mocked.act.mockResolvedValue({message: '🔴 Checked out', me: me({today_team_seconds: 3725})});
    await userEvent.click(screen.getByText('🔴 Check out'));
    await userEvent.click(screen.getByText('Yes, check out'));
    expect(mocked.act).toHaveBeenLastCalledWith(TEAM, 'out', '');
    expect(await screen.findByText('Not checked in')).toBeInTheDocument();
});

test('on break: the timer stands still', async () => {
    jest.useFakeTimers({now: Date.parse('2026-11-27T05:10:00Z')});
    mocked.fetchMe.mockResolvedValue(me({session: working({on_break: true, break_since: '2026-11-27T05:00:00Z'})}));
    renderPanel();
    expect(await screen.findByLabelText('Time worked this session')).toHaveTextContent('1:02:05');
    reactAct(() => {
        jest.advanceTimersByTime(5000);
    });
    expect(screen.getByLabelText('Time worked this session')).toHaveTextContent('1:02:05');
    expect(screen.getByText(/break 0h 10m/)).toBeInTheDocument();
});

test('checked in to another team', async () => {
    mocked.fetchMe.mockResolvedValue(me({session: working({team: {id: 'otherteamotherteamotherte', name: 'InkYank'}})}));
    renderPanel();
    expect(await screen.findByText(/in InkYank/)).toBeInTheDocument();
});

test('errors: not an attendance team, retry; action failures shown', async () => {
    mocked.fetchMe.mockRejectedValueOnce(new Error('Attendance isn’t used in this team.'));
    renderPanel();
    expect(await screen.findByRole('alert')).toHaveTextContent('Attendance isn’t used in this team.');

    mocked.fetchMe.mockResolvedValue(me());
    await userEvent.click(screen.getByText('Try again'));
    expect(await screen.findByText('Not checked in')).toBeInTheDocument();

    mocked.act.mockRejectedValue(new Error('Couldn’t reach the attendance service. Please try again.'));
    await userEvent.click(screen.getByText('▶ Check in'));
    expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t reach the attendance service.');
    expect(screen.getByText('Not checked in')).toBeInTheDocument();
});

test('live events reload the panel and clear an old reply', async () => {
    mocked.fetchMe.mockResolvedValue(me());
    mocked.act.mockResolvedValue({message: '✅ Checked in', me: me({session: working()})});
    renderPanel();
    expect(await screen.findByText('Not checked in')).toBeInTheDocument();
    await userEvent.click(screen.getByText('▶ Check in'));
    expect(await screen.findByRole('status')).toHaveTextContent('Checked in');

    mocked.fetchMe.mockResolvedValue(me({session: working()}));
    reactAct(() => {
        window.dispatchEvent(new Event(REFRESH_EVENT));
    });
    await waitFor(() => expect(screen.getByText('Working')).toBeInTheDocument());
    expect(mocked.fetchMe).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
});
