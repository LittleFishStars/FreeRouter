---
productId: material-ui
title: Menubar React component
components: Box, Button, Divider, List, ListItemButton, ListItemIcon, ListItemText, ListSubheader, Paper, Typography
---

# Menubar

A React component for building accessible application menus.



The menu bar is a visually persistent menu similar to those found in desktop applications that provides a consistent set of frequently used commands.

Material UI does not include a menu bar component out of the box, but this page provides components composed with the [Base UI `Menubar`](https://base-ui.com/react/components/menubar) and styled to align with Material Design (MD2) specifications, so they can be used with Material UI.

As such, you must install Base UI before proceeding.
The examples that follow can then be copied and pasted directly into your app.
Note that Base UI is tree-shakeable, so the final bundle will only include the components used in your project.

<!-- #npm-tag-reference -->

<codeblock storageKey="package-manager">

```bash npm
npm install @base-ui/react
```

```bash pnpm
pnpm add @base-ui/react
```

```bash yarn
yarn add @base-ui/react
```

</codeblock>

## Usage

1. Select one of the demos below that fits your visual design needs.
2. Click **Expand code** in the toolbar.
3. Select the file that starts with `./Menubar`.
4. Copy the code and paste it into your project.

## Basic menubar

The basic menubar component displays a row of menu triggers with dropdowns containing menu items.

```tsx
import * as React from 'react';
import {
  Menubar,
  MenuRoot,
  MenuTrigger,
  MenuPortal,
  MenuPositioner,
  MenuPopup,
  MenuItem,
  MenuSeparator,
  MenuSubmenuRoot,
  MenuSubmenuTrigger,
} from './components/Menubar';

export default function BasicMenubar() {
  return (
    <Menubar>
      <MenuRoot>
        <MenuTrigger>File</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuItem>New</MenuItem>
              <MenuItem>Open…</MenuItem>
              <MenuItem>Save</MenuItem>
              <MenuItem>Save as…</MenuItem>
              <MenuSeparator />
              <MenuSubmenuRoot>
                <MenuSubmenuTrigger>Share</MenuSubmenuTrigger>
                <MenuPortal>
                  <MenuPositioner alignOffset={-4}>
                    <MenuPopup>
                      <MenuItem>Email link</MenuItem>
                      <MenuItem>Copy link</MenuItem>
                    </MenuPopup>
                  </MenuPositioner>
                </MenuPortal>
              </MenuSubmenuRoot>
              <MenuSeparator />
              <MenuItem>Close</MenuItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>

      <MenuRoot>
        <MenuTrigger>Help</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4}>
            <MenuPopup>
              <MenuItem>Documentation</MenuItem>
              <MenuItem>Release notes</MenuItem>
              <MenuSeparator />
              <MenuItem>About</MenuItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>
    </Menubar>
  );
}

```

## Shortcut hints

Display keyboard shortcuts alongside menu items using the `hint` prop.

```tsx
import * as React from 'react';
import {
  Menubar,
  MenuRoot,
  MenuTrigger,
  MenuPortal,
  MenuPositioner,
  MenuPopup,
  MenuItem,
  MenuSeparator,
} from './components/Menubar';

export default function ShortcutHintsMenubar() {
  return (
    <Menubar>
      <MenuRoot>
        <MenuTrigger>File</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuItem hint="⌘N">New</MenuItem>
              <MenuItem hint="⌘O">Open…</MenuItem>
              <MenuItem hint="⌘S">Save</MenuItem>
              <MenuItem hint="⇧⌘S">Save As…</MenuItem>
              <MenuSeparator />
              <MenuItem hint="⌘P">Print…</MenuItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>

      <MenuRoot>
        <MenuTrigger>Edit</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuItem hint="⌘Z">Undo</MenuItem>
              <MenuItem hint="⇧⌘Z">Redo</MenuItem>
              <MenuSeparator />
              <MenuItem hint="⌘X">Cut</MenuItem>
              <MenuItem hint="⌘C">Copy</MenuItem>
              <MenuItem hint="⌘V">Paste</MenuItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>
    </Menubar>
  );
}

```

## Checkbox items

For toggle options that can be independently enabled or disabled.

```tsx
import * as React from 'react';
import {
  Menubar,
  MenuRoot,
  MenuTrigger,
  MenuPortal,
  MenuPositioner,
  MenuPopup,
  MenuItem,
  MenuCheckboxItem,
  MenuSeparator,
} from './components/Menubar';

export default function CheckboxItemsMenubar() {
  const [showSidebar, setShowSidebar] = React.useState(true);
  const [showToolbar, setShowToolbar] = React.useState(true);
  const [showStatusBar, setShowStatusBar] = React.useState(false);
  const [googleTasks, setGoogleTasks] = React.useState(true);
  const [mail, setMail] = React.useState(false);
  const [messages, setMessages] = React.useState(false);

  return (
    <Menubar>
      <MenuRoot>
        <MenuTrigger>View</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuCheckboxItem
                checked={showSidebar}
                onCheckedChange={setShowSidebar}
              >
                Show Sidebar
              </MenuCheckboxItem>
              <MenuCheckboxItem
                checked={showToolbar}
                onCheckedChange={setShowToolbar}
              >
                Show Toolbar
              </MenuCheckboxItem>
              <MenuCheckboxItem
                checked={showStatusBar}
                onCheckedChange={setShowStatusBar}
              >
                Show Status Bar
              </MenuCheckboxItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>

      <MenuRoot>
        <MenuTrigger>Window</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuItem hint="⌘M">Minimize</MenuItem>
              <MenuItem>Zoom</MenuItem>
              <MenuItem hint="⌃⌥F">Fill</MenuItem>
              <MenuItem hint="⌃⌥C">Center</MenuItem>
              <MenuSeparator />
              <MenuItem>Bring All to Front</MenuItem>
              <MenuSeparator />
              <MenuCheckboxItem
                checked={googleTasks}
                onCheckedChange={setGoogleTasks}
              >
                Google Tasks - Tasks
              </MenuCheckboxItem>
              <MenuCheckboxItem checked={mail} onCheckedChange={setMail}>
                Mail
              </MenuCheckboxItem>
              <MenuCheckboxItem checked={messages} onCheckedChange={setMessages}>
                Messages
              </MenuCheckboxItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>
    </Menubar>
  );
}

```

## Radio group items

For mutually exclusive options where only one can be selected at a time.

```tsx
import * as React from 'react';
import {
  Menubar,
  MenuRoot,
  MenuTrigger,
  MenuPortal,
  MenuPositioner,
  MenuPopup,
  MenuRadioGroup,
  MenuRadioItem,
  MenuSeparator,
  MenuSubmenuRoot,
  MenuSubmenuTrigger,
} from './components/Menubar';

export default function RadioGroupItemsMenubar() {
  const [theme, setTheme] = React.useState('system');
  const [density, setDensity] = React.useState('compact');

  return (
    <Menubar>
      <MenuRoot>
        <MenuTrigger>Appearance</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuSubmenuRoot>
                <MenuSubmenuTrigger>Theme</MenuSubmenuTrigger>
                <MenuPortal>
                  <MenuPositioner alignOffset={-4}>
                    <MenuPopup>
                      <MenuRadioGroup value={theme} onValueChange={setTheme}>
                        <MenuRadioItem value="light">Light</MenuRadioItem>
                        <MenuRadioItem value="dark">Dark</MenuRadioItem>
                        <MenuRadioItem value="system">System</MenuRadioItem>
                      </MenuRadioGroup>
                    </MenuPopup>
                  </MenuPositioner>
                </MenuPortal>
              </MenuSubmenuRoot>
              <MenuSeparator />
              <MenuRadioGroup value={density} onValueChange={setDensity}>
                <MenuRadioItem value="compact">Compact</MenuRadioItem>
                <MenuRadioItem value="comfortable">Comfortable</MenuRadioItem>
              </MenuRadioGroup>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>
    </Menubar>
  );
}

```

## Icon menu items

Add icons to menu items using the `icon` prop for faster visual recognition.

```tsx
import * as React from 'react';
import AddIcon from '@mui/icons-material/Add';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import SaveIcon from '@mui/icons-material/Save';
import PrintIcon from '@mui/icons-material/Print';
import UndoIcon from '@mui/icons-material/Undo';
import RedoIcon from '@mui/icons-material/Redo';
import {
  Menubar,
  MenuRoot,
  MenuTrigger,
  MenuPortal,
  MenuPositioner,
  MenuPopup,
  MenuItem,
  MenuSeparator,
} from './components/Menubar';

export default function IconItemsMenubar() {
  return (
    <Menubar>
      <MenuRoot>
        <MenuTrigger>File</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuItem icon={<AddIcon fontSize="small" />}>New</MenuItem>
              <MenuItem icon={<FolderOpenIcon fontSize="small" />}>Open…</MenuItem>
              <MenuItem icon={<SaveIcon fontSize="small" />}>Save</MenuItem>
              <MenuSeparator />
              <MenuItem icon={<PrintIcon fontSize="small" />}>Print…</MenuItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>

      <MenuRoot>
        <MenuTrigger>Edit</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuItem icon={<UndoIcon fontSize="small" />}>Undo</MenuItem>
              <MenuItem icon={<RedoIcon fontSize="small" />}>Redo</MenuItem>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>
    </Menubar>
  );
}

```

## Group labels

Organize menu items into logical sections with labeled groups.

```tsx
import * as React from 'react';
import InfoIcon from '@mui/icons-material/Info';
import SettingsIcon from '@mui/icons-material/Settings';
import BuildIcon from '@mui/icons-material/Build';
import MonitorHeartIcon from '@mui/icons-material/MonitorHeart';
import TimelineIcon from '@mui/icons-material/Timeline';
import InsertDriveFileIcon from '@mui/icons-material/InsertDriveFile';
import ToggleOnIcon from '@mui/icons-material/ToggleOn';
import TuneIcon from '@mui/icons-material/Tune';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import VisibilityIcon from '@mui/icons-material/Visibility';
import UndoIcon from '@mui/icons-material/Undo';
import RedoIcon from '@mui/icons-material/Redo';
import ContentCutIcon from '@mui/icons-material/ContentCut';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import ContentPasteIcon from '@mui/icons-material/ContentPaste';
import {
  Menubar,
  MenuRoot,
  MenuTrigger,
  MenuPortal,
  MenuPositioner,
  MenuPopup,
  MenuItem,
  MenuSeparator,
  MenuSubmenuRoot,
  MenuSubmenuTrigger,
  MenuGroup,
  MenuGroupLabel,
} from './components/Menubar';

export default function GroupLabelMenubar() {
  return (
    <Menubar>
      <MenuRoot>
        <MenuTrigger>App</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4} alignOffset={-2}>
            <MenuPopup>
              <MenuGroup>
                <MenuGroupLabel>General</MenuGroupLabel>
                <MenuItem icon={<InfoIcon fontSize="small" />}>About</MenuItem>
                <MenuItem icon={<SettingsIcon fontSize="small" />}>
                  Settings...
                </MenuItem>
              </MenuGroup>
              <MenuSeparator />
              <MenuSubmenuRoot>
                <MenuSubmenuTrigger icon={<BuildIcon fontSize="small" />}>
                  Services
                </MenuSubmenuTrigger>
                <MenuPortal>
                  <MenuPositioner alignOffset={-4}>
                    <MenuPopup>
                      <MenuGroup>
                        <MenuGroupLabel>Development</MenuGroupLabel>
                        <MenuItem icon={<MonitorHeartIcon fontSize="small" />}>
                          Activity Monitor
                        </MenuItem>
                        <MenuItem icon={<TimelineIcon fontSize="small" />}>
                          System Trace
                        </MenuItem>
                        <MenuItem icon={<InsertDriveFileIcon fontSize="small" />}>
                          File Activity
                        </MenuItem>
                      </MenuGroup>
                      <MenuSeparator />
                      <MenuGroup>
                        <MenuGroupLabel>Shortcuts</MenuGroupLabel>
                        <MenuItem icon={<ToggleOnIcon fontSize="small" />}>
                          Toggle Gate
                        </MenuItem>
                        <MenuItem icon={<TuneIcon fontSize="small" />}>
                          Services Settings...
                        </MenuItem>
                      </MenuGroup>
                    </MenuPopup>
                  </MenuPositioner>
                </MenuPortal>
              </MenuSubmenuRoot>
              <MenuSeparator />
              <MenuGroup>
                <MenuGroupLabel>Window</MenuGroupLabel>
                <MenuItem icon={<VisibilityOffIcon fontSize="small" />}>
                  Hide App
                </MenuItem>
                <MenuItem icon={<VisibilityOffIcon fontSize="small" />}>
                  Hide Others
                </MenuItem>
                <MenuItem icon={<VisibilityIcon fontSize="small" />}>
                  Show All
                </MenuItem>
              </MenuGroup>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>

      <MenuRoot>
        <MenuTrigger>Edit</MenuTrigger>
        <MenuPortal>
          <MenuPositioner sideOffset={4}>
            <MenuPopup>
              <MenuGroup>
                <MenuGroupLabel>History</MenuGroupLabel>
                <MenuItem icon={<UndoIcon fontSize="small" />}>Undo</MenuItem>
                <MenuItem icon={<RedoIcon fontSize="small" />}>Redo</MenuItem>
              </MenuGroup>
              <MenuSeparator />
              <MenuGroup>
                <MenuGroupLabel>Clipboard</MenuGroupLabel>
                <MenuItem icon={<ContentCutIcon fontSize="small" />}>Cut</MenuItem>
                <MenuItem icon={<ContentCopyIcon fontSize="small" />}>Copy</MenuItem>
                <MenuItem icon={<ContentPasteIcon fontSize="small" />}>
                  Paste
                </MenuItem>
              </MenuGroup>
            </MenuPopup>
          </MenuPositioner>
        </MenuPortal>
      </MenuRoot>
    </Menubar>
  );
}

```

## Base UI API

See the documentation below for a complete reference to all of the props.

- [`Menubar`](https://base-ui.com/react/components/menubar#api-reference)
- [`Menu`](https://base-ui.com/react/components/menu#api-reference)
