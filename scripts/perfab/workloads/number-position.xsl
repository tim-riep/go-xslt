<!-- xsl:number (single and multi level) and position()/last() in templates over all records -->
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/"><xsl:apply-templates select="orders/order"/></xsl:template>
  <xsl:template match="order">
    <xsl:number format="1. "/>
    <xsl:value-of select="concat(position(), '/', last(), ' ')"/>
    <xsl:apply-templates select="items/item"/>
    <xsl:text>&#10;</xsl:text>
  </xsl:template>
  <xsl:template match="item">
    <xsl:number level="multiple" count="order|item" format="1.a "/>
    <xsl:if test="position() = last()"><xsl:text>end</xsl:text></xsl:if>
  </xsl:template>
</xsl:stylesheet>
