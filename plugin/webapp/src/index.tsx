// The Attendance plugin's web part: an app-bar icon that opens the panel in
// the right-hand side, and live updates from the server. See ui-plan.md.

// Compass tokens are declared in cascade layers, so this is safe to load next to the host's
// copy. Never import '@mattermost/compass-ui/styles/standalone' here: it overrides the user's theme.
import '@mattermost/compass-ui/styles';

import {publicURL, setSiteURL} from 'client';
import manifest from 'manifest';
import Panel from 'panel/Panel';
import {ALL, requestRefresh} from 'panel/refresh';
import type {Store} from 'redux';

import type {GlobalState} from '@mattermost/types/store';

import {getConfig} from 'mattermost-redux/selectors/entities/general';

import type {PluginRegistry} from 'types/mattermost-webapp';

// Events the bot sends (through the plugin) when something changed.
export const LIVE_EVENTS = ['session_changed', 'tasks_changed', 'eod_changed', 'wfh_changed', 'team_changed'];

export default class Plugin {
    public async initialize(registry: PluginRegistry, store: Store<GlobalState>) {
        setSiteURL(getConfig(store.getState()).SiteURL);

        // The panel lives in the right-hand side; the app-bar icon toggles it.
        const {toggleRHSPlugin} = registry.registerRightHandSidebarComponent(Panel, 'Attendance');
        registry.registerAppBarComponent({
            iconUrl: publicURL('icon.svg'),
            action: () => store.dispatch(toggleRHSPlugin),
            tooltipText: 'Attendance',
            supportedProductIds: null,
        });

        for (const ev of LIVE_EVENTS) {
            registry.registerWebSocketEventHandler<{team_id?: string}>(
                `custom_${manifest.id}_${ev}`,
                (msg) => requestRefresh(ev, msg.data?.team_id),
            );
        }

        // Events sent while disconnected are lost: reload everything when we're back.
        registry.registerReconnectHandler(() => requestRefresh(ALL));
    }
}

declare global {
    interface Window {
        registerPlugin(pluginId: string, plugin: Plugin): void;
    }
}

window.registerPlugin(manifest.id, new Plugin());
