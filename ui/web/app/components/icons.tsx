type IconProps = {
  className?: string;
};

export function BrandMark({ className }: IconProps) {
  return (
    <svg
      className={className}
      viewBox="0 0 32 32"
      aria-hidden="true"
      focusable="false"
    >
      <rect className="brand-mark-field" x="1" y="1" width="30" height="30" rx="9" />
      <path
        className="brand-mark-path"
        d="M22 9.2c0-2.3-1.9-4.2-4.2-4.2h-5.1a4.2 4.2 0 0 0 0 8.4h5.1a4.2 4.2 0 0 1 0 8.4h-5.6A4.2 4.2 0 0 1 8 17.6"
      />
      <circle className="brand-mark-eye" cx="22" cy="8.2" r="1.15" />
    </svg>
  );
}

export function PlusIcon({ className }: IconProps) {
  return (
    <svg className={className} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

export function SendIcon({ className }: IconProps) {
  return (
    <svg className={className} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M12 19V5M6.5 10.5 12 5l5.5 5.5" />
    </svg>
  );
}

export function StopIcon({ className }: IconProps) {
  return (
    <svg className={className} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <rect x="7" y="7" width="10" height="10" rx="1.5" />
    </svg>
  );
}

export function ToolIcon({ className }: IconProps) {
  return (
    <svg className={className} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="m5 8 4 4-4 4M11.5 16.5h7.5" />
    </svg>
  );
}

export function TrashIcon({ className }: IconProps) {
  return (
    <svg className={className} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M5 7h14M9 7V4h6v3M8 10v7M12 10v7M16 10v7M7 7l1 13h8l1-13" />
    </svg>
  );
}
