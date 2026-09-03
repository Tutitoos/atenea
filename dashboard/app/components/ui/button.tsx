import type { ButtonHTMLAttributes } from "react";
import { Button as BaseButton } from "@base-ui/react/button";
import { cn } from "~/lib/cn";

export function Button({ className, variant = "default", size = "default", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "default" | "outline" | "ghost" | "secondary"; size?: "default" | "sm" | "icon" }) {
  return <BaseButton className={cn("inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors focus-visible:ring-2 disabled:pointer-events-none disabled:opacity-50", variant === "default" && "bg-primary text-primary-foreground hover:bg-primary/90", variant === "outline" && "border bg-background hover:bg-accent hover:text-accent-foreground", variant === "ghost" && "hover:bg-accent hover:text-accent-foreground", variant === "secondary" && "bg-secondary text-secondary-foreground hover:bg-secondary/80", size === "default" && "h-11 px-4", size === "sm" && "h-9 px-3 text-xs", size === "icon" && "h-11 w-11", className)} {...props} />;
}
