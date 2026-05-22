if string.match(msg, "^%s*出勤%s*$") or string.match(msg, "^%s*退勤%s*$") then
    return true
else
    return false
end
