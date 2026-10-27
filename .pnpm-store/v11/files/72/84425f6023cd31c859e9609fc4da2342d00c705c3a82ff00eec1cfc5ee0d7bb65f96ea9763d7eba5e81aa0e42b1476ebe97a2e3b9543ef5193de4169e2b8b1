---
productId: material-ui
title: React Badge component
components: Badge
githubLabel: 'scope: badge'
githubSource: packages/mui-material/src/Badge
---

# Badge

Badge generates a small badge to the top-right of its child(ren).



## Usage guidelines

- **Use badges for supplemental status**: Use badges for short counts or compact states
  that update an existing control or item, such as unread messages on an inbox button. If
  the status is important on its own, show it in the UI instead of relying only on the badge.
- **Label the element that owns the badge**: A badge is a visual cue tied to another
  element, so its meaning should be part of that element's accessible name. For example,
  use `aria-label="Inbox, 4 unread messages"` instead of `aria-label="Inbox"` on the
  target element.
- **Use dot badges for simple states**: A dot badge does not show text or a number, so use
  it only when the surrounding UI makes the state clear, such as `Online` or `Unread`.

```tsx
import IconButton from '@mui/material/IconButton';
import Badge from '@mui/material/Badge';
import MailIcon from '@mui/icons-material/Mail';

const maxVisibleNotifications = 99;
const unreadNotificationsCount = 100;

function getUnreadNotificationsLabel(count: number) {
  if (count === 0) {
    return 'show no unread notifications';
  }
  if (count > maxVisibleNotifications) {
    return `show more than ${maxVisibleNotifications} unread notifications`;
  }
  return `show ${count} unread notification${count === 1 ? '' : 's'}`;
}

export default function BadgeIntro() {
  const label = getUnreadNotificationsLabel(unreadNotificationsCount);

  return (
    <IconButton aria-label={label}>
      <Badge
        badgeContent={unreadNotificationsCount}
        color="secondary"
        max={maxVisibleNotifications}
      >
        <MailIcon />
      </Badge>
    </IconButton>
  );
}

```

This demo applies the same pattern to a `ListItemButton` with a `Badge`: the visible
count is included in the item's accessible name so it's announced in the context of the
surrounding UI.

```tsx
import Badge from '@mui/material/Badge';
import List from '@mui/material/List';
import ListItemButton from '@mui/material/ListItemButton';
import ListItemIcon from '@mui/material/ListItemIcon';
import ListItemText from '@mui/material/ListItemText';
import Paper from '@mui/material/Paper';
import DraftsIcon from '@mui/icons-material/Drafts';
import InboxIcon from '@mui/icons-material/Inbox';
import SendIcon from '@mui/icons-material/Send';

const unreadMessagesCount = 4;

export default function BadgeListItem() {
  return (
    <Paper variant="outlined" sx={{ width: 320, maxWidth: '100%' }}>
      <List component="nav" aria-label="mail folders" sx={{ py: 0 }}>
        <ListItemButton
          selected
          aria-current="page"
          aria-label={`Inbox, ${unreadMessagesCount} unread messages`}
        >
          <ListItemIcon>
            <Badge badgeContent={unreadMessagesCount} color="primary">
              <InboxIcon />
            </Badge>
          </ListItemIcon>
          <ListItemText primary="Inbox" />
        </ListItemButton>
        <ListItemButton>
          <ListItemIcon>
            <SendIcon />
          </ListItemIcon>
          <ListItemText primary="Sent" />
        </ListItemButton>
        <ListItemButton>
          <ListItemIcon>
            <DraftsIcon />
          </ListItemIcon>
          <ListItemText primary="Drafts" />
        </ListItemButton>
      </List>
    </Paper>
  );
}

```

## Badge content

Use `badgeContent` to add a short count or label to the wrapped element.

```tsx
import Badge from '@mui/material/Badge';
import IconButton from '@mui/material/IconButton';
import MailIcon from '@mui/icons-material/Mail';

export default function SimpleBadge() {
  return (
    <IconButton aria-label="show 4 unread messages">
      <Badge badgeContent={4} color="primary">
        <MailIcon />
      </Badge>
    </IconButton>
  );
}

```

### Dot badge

Use `variant="dot"` for a compact status indicator without a count.

```tsx
import IconButton from '@mui/material/IconButton';
import Badge from '@mui/material/Badge';
import NotificationsIcon from '@mui/icons-material/Notifications';

export default function DotBadge() {
  return (
    <IconButton aria-label="show new notifications">
      <Badge color="secondary" variant="dot">
        <NotificationsIcon />
      </Badge>
    </IconButton>
  );
}

```

### Visibility

Control badge visibility with the `invisible` prop.

```tsx
import * as React from 'react';
import Stack from '@mui/material/Stack';
import Badge from '@mui/material/Badge';
import IconButton from '@mui/material/IconButton';
import Switch from '@mui/material/Switch';
import FormControlLabel from '@mui/material/FormControlLabel';
import MailIcon from '@mui/icons-material/Mail';

const unreadMessagesCount = 4;

export default function BadgeVisibility() {
  const [invisible, setInvisible] = React.useState(false);

  const handleBadgeVisibility = () => {
    setInvisible((previousInvisible) => !previousInvisible);
  };

  return (
    <Stack direction="row" spacing={3} sx={{ alignItems: 'center' }}>
      <IconButton
        aria-label={
          invisible
            ? 'open inbox'
            : `open inbox, ${unreadMessagesCount} unread messages`
        }
      >
        <Badge
          color="secondary"
          badgeContent={unreadMessagesCount}
          invisible={invisible}
        >
          <MailIcon />
        </Badge>
      </IconButton>
      <FormControlLabel
        control={<Switch checked={!invisible} onChange={handleBadgeVisibility} />}
        label="Show unread count"
      />
    </Stack>
  );
}

```

The badge hides automatically when `badgeContent` is zero. Override this with the `showZero`
prop when zero is meaningful to the interface.

```tsx
import Stack from '@mui/material/Stack';
import Badge from '@mui/material/Badge';
import IconButton from '@mui/material/IconButton';
import MailIcon from '@mui/icons-material/Mail';

export default function ShowZeroBadge() {
  return (
    <Stack spacing={2} direction="row">
      <IconButton aria-label="show no unread messages">
        <Badge color="secondary" badgeContent={0}>
          <MailIcon />
        </Badge>
      </IconButton>
      <IconButton aria-label="show 0 unread messages">
        <Badge color="secondary" badgeContent={0} showZero>
          <MailIcon />
        </Badge>
      </IconButton>
    </Stack>
  );
}

```

### Maximum value

Use the `max` prop to cap large numeric values.

```tsx
import Stack from '@mui/material/Stack';
import Badge from '@mui/material/Badge';
import IconButton from '@mui/material/IconButton';
import MailIcon from '@mui/icons-material/Mail';

export default function BadgeMax() {
  return (
    <Stack spacing={2} direction="row">
      <IconButton aria-label="show 99 unread messages">
        <Badge color="secondary" badgeContent={99}>
          <MailIcon />
        </Badge>
      </IconButton>
      <IconButton aria-label="show more than 99 unread messages">
        <Badge color="secondary" badgeContent={100}>
          <MailIcon />
        </Badge>
      </IconButton>
      <IconButton aria-label="show more than 999 unread messages">
        <Badge color="secondary" badgeContent={1000} max={999}>
          <MailIcon />
        </Badge>
      </IconButton>
    </Stack>
  );
}

```

## Customization

### Color

Use the `color` prop to apply theme palette colors to the badge.

```tsx
import Badge from '@mui/material/Badge';
import Stack from '@mui/material/Stack';
import IconButton from '@mui/material/IconButton';
import CalendarMonthIcon from '@mui/icons-material/CalendarMonth';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import ReportProblemOutlinedIcon from '@mui/icons-material/ReportProblemOutlined';

export default function ColorBadge() {
  return (
    <Stack spacing={2} direction="row">
      <IconButton aria-label="show 8 shared files">
        <Badge badgeContent={8} color="primary">
          <FolderOpenIcon />
        </Badge>
      </IconButton>
      <IconButton aria-label="show 2 confirmed events">
        <Badge badgeContent={2} color="success">
          <CalendarMonthIcon />
        </Badge>
      </IconButton>
      <IconButton aria-label="show 1 critical alert">
        <Badge badgeContent={1} color="error">
          <ReportProblemOutlinedIcon />
        </Badge>
      </IconButton>
    </Stack>
  );
}

```

### Badge alignment

Use the `anchorOrigin` prop to move the badge to any corner of the wrapped element.

```tsx
import * as React from 'react';
import Badge from '@mui/material/Badge';
import FormControl from '@mui/material/FormControl';
import FormControlLabel from '@mui/material/FormControlLabel';
import FormLabel from '@mui/material/FormLabel';
import Radio from '@mui/material/Radio';
import RadioGroup from '@mui/material/RadioGroup';
import Box from '@mui/material/Box';
import IconButton from '@mui/material/IconButton';
import MailIcon from '@mui/icons-material/Mail';
import { HighlightedCode } from '@mui/internal-core-docs/HighlightedCode';

type BadgeVerticalOrigin = 'top' | 'bottom';
type BadgeHorizontalOrigin = 'right' | 'left';

export default function BadgeAlignment() {
  const [horizontal, setHorizontal] = React.useState<BadgeHorizontalOrigin>('right');
  const [vertical, setVertical] = React.useState<BadgeVerticalOrigin>('top');

  const handleHorizontalChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    setHorizontal(event.target.value as BadgeHorizontalOrigin);
  };

  const handleVerticalChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    setVertical(event.target.value as BadgeVerticalOrigin);
  };

  const jsx = `
<IconButton aria-label="show 12 unread messages">
  <Badge
    badgeContent={12}
    color="secondary"
    anchorOrigin={{
      vertical: '${vertical}',
      horizontal: '${horizontal}',
    }}
  >
    <MailIcon />
  </Badge>
</IconButton>
`;

  return (
    <Box sx={{ width: '100%' }}>
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'center',
          '& fieldset': {
            margin: 3,
          },
        }}
      >
        <FormControl component="fieldset">
          <FormLabel component="legend">Vertical</FormLabel>
          <RadioGroup
            name="vertical"
            value={vertical}
            onChange={handleVerticalChange}
          >
            <FormControlLabel value="top" control={<Radio />} label="Top" />
            <FormControlLabel value="bottom" control={<Radio />} label="Bottom" />
          </RadioGroup>
        </FormControl>
        <FormControl component="fieldset">
          <FormLabel component="legend">Horizontal</FormLabel>
          <RadioGroup
            name="horizontal"
            value={horizontal}
            onChange={handleHorizontalChange}
          >
            <FormControlLabel value="right" control={<Radio />} label="Right" />
            <FormControlLabel value="left" control={<Radio />} label="Left" />
          </RadioGroup>
        </FormControl>
      </Box>
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'center',
          color: 'action.active',
          '& > *': {
            margin: 2,
          },
        }}
      >
        <IconButton aria-label="show unread messages">
          <Badge
            color="secondary"
            variant="dot"
            anchorOrigin={{
              horizontal,
              vertical,
            }}
          >
            <MailIcon />
          </Badge>
        </IconButton>
        <IconButton aria-label="show 1 unread message">
          <Badge
            color="secondary"
            badgeContent={1}
            anchorOrigin={{
              horizontal,
              vertical,
            }}
          >
            <MailIcon />
          </Badge>
        </IconButton>
        <IconButton aria-label="show 12 unread messages">
          <Badge
            color="secondary"
            badgeContent={12}
            anchorOrigin={{
              horizontal,
              vertical,
            }}
          >
            <MailIcon />
          </Badge>
        </IconButton>
        <IconButton aria-label="show more than 99 unread messages">
          <Badge
            color="secondary"
            badgeContent={123}
            anchorOrigin={{
              horizontal,
              vertical,
            }}
          >
            <MailIcon />
          </Badge>
        </IconButton>
        <IconButton aria-label="show more than 999 unread messages">
          <Badge
            color="secondary"
            max={999}
            badgeContent={1337}
            anchorOrigin={{
              horizontal,
              vertical,
            }}
          >
            <MailIcon />
          </Badge>
        </IconButton>
      </Box>
      <HighlightedCode code={jsx} language="jsx" />
    </Box>
  );
}

```

### Badge overlap

Use the `overlap` prop when the wrapped element is circular.

```tsx
import Box from '@mui/material/Box';
import Stack from '@mui/material/Stack';
import Badge from '@mui/material/Badge';

const shapeSize = 32;

export default function BadgeOverlap() {
  return (
    <Stack spacing={3} direction="row">
      <Badge color="secondary" badgeContent={1}>
        <Rectangle />
      </Badge>
      <Badge color="secondary" variant="dot">
        <Rectangle />
      </Badge>
      <Badge color="secondary" overlap="circular" badgeContent={1}>
        <Circle />
      </Badge>
      <Badge color="secondary" overlap="circular" variant="dot">
        <Circle />
      </Badge>
    </Stack>
  );
}

function Rectangle() {
  return (
    <Box
      component="span"
      sx={{ bgcolor: 'primary.main', width: shapeSize, height: shapeSize }}
    />
  );
}

function Circle() {
  return (
    <Box
      component="span"
      sx={{
        bgcolor: 'primary.main',
        width: shapeSize,
        height: shapeSize,
        borderRadius: '50%',
      }}
    />
  );
}

```

### Custom styles

Use theme style overrides, the `sx` prop, or `styled()` to customize the badge.
Learn more in the [customization guide](/material-ui/customization/how-to-customize/).

```tsx
import Badge from '@mui/material/Badge';
import Avatar from '@mui/material/Avatar';
import List from '@mui/material/List';
import ListItemAvatar from '@mui/material/ListItemAvatar';
import ListItemButton from '@mui/material/ListItemButton';
import ListItemText from '@mui/material/ListItemText';
import Paper from '@mui/material/Paper';
import { styled } from '@mui/material/styles';

type ContactStatus = 'online' | 'offline';

const statusLabels: Record<ContactStatus, string> = {
  online: 'Online',
  offline: 'Offline',
};

const contacts = [
  { name: 'Remy Sharp', initials: 'R', status: 'online' },
  { name: 'Travis Howard', initials: 'T', status: 'offline' },
  { name: 'Cindy Baker', initials: 'C', status: 'online' },
] as const;

const ContactStatusBadge = styled(Badge, {
  shouldForwardProp: (prop) => prop !== 'status',
})<{ status: ContactStatus }>(({ theme, status }) => {
  const themePalette = (theme.vars ?? theme).palette;
  const offlineBadgeColor = theme.vars
    ? theme.vars.palette.Avatar.defaultBg
    : theme.palette.grey[400];

  return {
    '& .MuiBadge-badge': {
      height: 10,
      minWidth: 10,
      border: `1px solid ${themePalette.grey[300]}`,
      boxShadow: `0 0 0 2px ${themePalette.background.paper}`,
      ...(status === 'offline' && {
        backgroundColor: offlineBadgeColor,
        ...(theme.vars
          ? {}
          : theme.applyStyles('dark', {
              backgroundColor: theme.palette.grey[600],
            })),
      }),
    },
  };
});

export default function CustomizedBadges() {
  return (
    <Paper variant="outlined" sx={{ width: 320, maxWidth: '100%' }}>
      <List component="nav" aria-label="contacts" sx={{ py: 0 }}>
        {contacts.map((contact) => {
          const statusLabel = statusLabels[contact.status];

          return (
            <ListItemButton
              key={contact.name}
              aria-label={`${contact.name}, ${contact.status}`}
            >
              <ListItemAvatar>
                <ContactStatusBadge
                  status={contact.status}
                  color={contact.status === 'online' ? 'success' : 'default'}
                  variant="dot"
                  overlap="circular"
                  anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
                >
                  <Avatar>{contact.initials}</Avatar>
                </ContactStatusBadge>
              </ListItemAvatar>
              <ListItemText primary={contact.name} secondary={statusLabel} />
            </ListItemButton>
          );
        })}
      </List>
    </Paper>
  );
}

```


# Badge API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Avatar](https://mui.com/material-ui/react-avatar/)
- [Badge](https://mui.com/material-ui/react-badge/)

## Import

```jsx
import Badge from '@mui/material/Badge';
// or
import { Badge } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| anchorOrigin | `{ horizontal?: 'left' \| 'right', vertical?: 'bottom' \| 'top' }` | `{
  vertical: 'top',
  horizontal: 'right',
}` | No |  |
| badgeContent | `node` | - | No |  |
| children | `node` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'default' \| 'primary' \| 'secondary' \| 'error' \| 'info' \| 'success' \| 'warning' \| string` | `'default'` | No |  |
| component | `elementType` | - | No |  |
| invisible | `bool` | `false` | No |  |
| max | `number` | `99` | No |  |
| overlap | `'circular' \| 'rectangular'` | `'rectangular'` | No |  |
| showZero | `bool` | `false` | No |  |
| slotProps | `{ badge?: func \| object, root?: func \| object }` | `{}` | No |  |
| slots | `{ badge?: elementType, root?: elementType }` | `{}` | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| variant | `'dot' \| 'standard' \| string` | `'standard'` | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLSpanElement).

> Any other props supplied will be provided to the root element (native element).

## Theme default props

You can use `MuiBadge` to change the default props of this component with the theme.

## Slots

| Name | Default | Class | Description |
|------|---------|-------|-------------|
| root | `span` | `.MuiBadge-root` | The component that renders the root. |
| badge | `span` | `.MuiBadge-badge` | The component that renders the badge. |

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | anchorOriginBottomLeft | Styles applied to the badge `span` element if `anchorOrigin={{ 'bottom', 'left' }}`. |
| - | anchorOriginBottomLeftCircular | Styles applied to the badge `span` element if `anchorOrigin={{ 'bottom', 'left' }} overlap="circular"`. |
| - | anchorOriginBottomLeftRectangular | Styles applied to the badge `span` element if `anchorOrigin={{ 'bottom', 'left' }} overlap="rectangular"`. |
| - | anchorOriginBottomRight | Styles applied to the badge `span` element if `anchorOrigin={{ 'bottom', 'right' }}`. |
| - | anchorOriginBottomRightCircular | Styles applied to the badge `span` element if `anchorOrigin={{ 'bottom', 'right' }} overlap="circular"`. |
| - | anchorOriginBottomRightRectangular | Styles applied to the badge `span` element if `anchorOrigin={{ 'bottom', 'right' }} overlap="rectangular"`. |
| - | anchorOriginTopLeft | Styles applied to the badge `span` element if `anchorOrigin={{ 'top', 'left' }}`. |
| - | anchorOriginTopLeftCircular | Styles applied to the badge `span` element if `anchorOrigin={{ 'top', 'left' }} overlap="circular"`. |
| - | anchorOriginTopLeftRectangular | Styles applied to the badge `span` element if `anchorOrigin={{ 'top', 'left' }} overlap="rectangular"`. |
| - | anchorOriginTopRight | Styles applied to the badge `span` element if `anchorOrigin={{ 'top', 'right' }}`. |
| - | anchorOriginTopRightCircular | Styles applied to the badge `span` element if `anchorOrigin={{ 'top', 'right' }} overlap="circular"`. |
| - | anchorOriginTopRightRectangular | Styles applied to the badge `span` element if `anchorOrigin={{ 'top', 'right' }} overlap="rectangular"`. |
| - | colorError | Styles applied to the badge `span` element if `color="error"`. |
| - | colorInfo | Styles applied to the badge `span` element if `color="info"`. |
| - | colorPrimary | Styles applied to the badge `span` element if `color="primary"`. |
| - | colorSecondary | Styles applied to the badge `span` element if `color="secondary"`. |
| - | colorSuccess | Styles applied to the badge `span` element if `color="success"`. |
| - | colorWarning | Styles applied to the badge `span` element if `color="warning"`. |
| - | dot | Styles applied to the badge `span` element if `variant="dot"`. |
| - | invisible | State class applied to the badge `span` element if `invisible={true}`. |
| - | overlapCircular | Styles applied to the badge `span` element if `overlap="circular"`. |
| - | overlapRectangular | Styles applied to the badge `span` element if `overlap="rectangular"`. |
| - | standard | Styles applied to the badge `span` element if `variant="standard"`. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/Badge/Badge.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/Badge/Badge.js)